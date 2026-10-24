package consents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// RevocationIDPrefix starts every revocation id; 128 random bits follow.
const RevocationIDPrefix = "rvk-"

// noticeKeyPrefix starts the idempotency key of the notice of a revocation to
// one entity: revocation-<consent>-<entity>, the same on every attempt.
const noticeKeyPrefix = "revocation"

// RevokeCommand is a revocation requested by any channel. The holder's app
// uses RevokeAsHolder; the compliance portal (HU-W4.2-02) builds the command
// with its actor, channel, reason and reference and calls Revoke.
type RevokeCommand struct {
	ConsentID string
	Actor     domain.Actor
	Channel   domain.RevocationChannel
	// Request carries the time of the request, the device and the
	// confirmation method. The portal sets RequestedAt to the server time.
	Request   domain.RevokeRequest
	Reason    string
	Reference string
	// RequestID is the idempotency key of the request.
	RequestID string
}

// RevocationService revokes consents (HU-M1.3-02) and is the process the
// compliance portal reuses (HU-W4.2-02): it records the immutable revocation
// and the queue of notices before answering, so the consent stops being
// verified as active at once, and it retries the entities that did not
// confirm.
type RevocationService struct {
	consents    ports.ConsentStore
	events      ports.ConsentEventReader
	revocations ports.RevocationStore
	notices     ports.NoticeStore
	receipts    ports.ReceiptRenderer
	clock       ports.Clock
	names       domain.EntityNames
	deadline    time.Duration
	wake        func()
}

// RevocationDeps are the ports of the RevocationService.
type RevocationDeps struct {
	Consents    ports.ConsentStore
	Events      ports.ConsentEventReader
	Revocations ports.RevocationStore
	Notices     ports.NoticeStore
	Receipts    ports.ReceiptRenderer
	Clock       ports.Clock
	// Names is the catalog of display names of the entities.
	Names domain.EntityNames
	// NoticeDeadline is how long the entities have to confirm; zero is
	// domain.RevocationNoticeDeadline (RN-02, 5 minutes).
	NoticeDeadline time.Duration
}

// NewRevocationService builds the service.
func NewRevocationService(d RevocationDeps) *RevocationService {
	deadline := d.NoticeDeadline
	if deadline <= 0 {
		deadline = domain.RevocationNoticeDeadline
	}
	return &RevocationService{
		consents: d.Consents, events: d.Events, revocations: d.Revocations, notices: d.Notices, receipts: d.Receipts,
		clock: d.Clock, names: d.Names, deadline: deadline,
	}
}

// OnNewNotices sets what to call when new notices are ready (a revocation or
// a retry was recorded): the notice worker wakes up at once instead of
// waiting for its next poll.
func (s *RevocationService) OnNewNotices(wake func()) { s.wake = wake }

// NoticeDeadline is how long the entities have to confirm a revocation.
func (s *RevocationService) NoticeDeadline() time.Duration { return s.deadline }

// RevokeAsHolder revokes a consent of the holder whose document is given, as
// requested from their app (criteria 1 to 4). A consent of another holder is
// answered like one that does not exist, ports.ErrConsentNotFound.
func (s *RevocationService) RevokeAsHolder(ctx context.Context, document, subject, consentID, requestID string,
	req domain.RevokeRequest) (domain.ConsentView, bool, error) {
	c, err := s.holderConsent(ctx, document, consentID)
	if err != nil {
		return domain.ConsentView{}, false, err
	}
	return s.revoke(ctx, c, RevokeCommand{
		ConsentID: consentID, Actor: domain.Actor{Type: domain.ActorHolder, ID: subject},
		Channel: domain.RevocationChannelMobileApp, Request: req, RequestID: requestID,
	})
}

// Revoke records the revocation of the command and returns the consent as it
// is now, with created=true. The same request again (same RequestID) returns
// the consent with created=false and records nothing. Another request on a
// revoked consent gives domain.ErrAlreadyRevoked, an expired consent
// domain.ErrConsentNotActive, an invalid request a *domain.ValidationError,
// and an unknown consent ports.ErrConsentNotFound.
func (s *RevocationService) Revoke(ctx context.Context, cmd RevokeCommand) (domain.ConsentView, bool, error) {
	c, err := s.consents.Get(ctx, cmd.ConsentID)
	if errors.Is(err, ports.ErrConsentNotFound) {
		return domain.ConsentView{}, false, err
	}
	if err != nil {
		return domain.ConsentView{}, false, fmt.Errorf("read the consent: %w", err)
	}
	return s.revoke(ctx, c, cmd)
}

func (s *RevocationService) revoke(ctx context.Context, c domain.Consent, cmd RevokeCommand) (domain.ConsentView, bool, error) {
	if cmd.RequestID == "" {
		return domain.ConsentView{}, false, &domain.ValidationError{Fields: []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeRequired}}}
	}
	// PostgreSQL keeps microseconds: the times the caller gets are the stored ones.
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	view, err := s.view(ctx, c, now)
	if err != nil {
		return domain.ConsentView{}, false, err
	}
	if err := domain.CheckRevocable(view); err != nil {
		if errors.Is(err, domain.ErrAlreadyRevoked) && s.sameRequest(ctx, c.ID, cmd.RequestID) {
			return view, false, nil
		}
		return domain.ConsentView{}, false, err
	}
	if err := cmd.Request.Validate(c, now); err != nil {
		return domain.ConsentView{}, false, err
	}
	id, err := newRevocationID()
	if err != nil {
		return domain.ConsentView{}, false, err
	}
	rev := domain.Revocation{
		ID: id, ConsentID: c.ID, Holder: c.Holder, Scope: c.Scope, Entities: view.Entities,
		RequestedAt: cmd.Request.RequestedAt.UTC().Truncate(time.Microsecond), EffectiveAt: now,
		NoticeDeadlineAt: now.Add(s.deadline), Channel: cmd.Channel, Device: cmd.Request.NormalizedDevice(),
		ConfirmationMethod: domain.ConfirmationMethod(cmd.Request.ConfirmationMethod), Actor: cmd.Actor,
		Reason: cmd.Reason, Reference: cmd.Reference, RequestID: cmd.RequestID,
	}
	notices := make([]ports.NoticeRecord, 0, len(rev.Entities))
	for _, e := range rev.Entities {
		key, err := idempotency.Key(noticeKeyPrefix, c.ID+"-"+e.Code)
		if err != nil {
			return domain.ConsentView{}, false, err
		}
		notices = append(notices, ports.NoticeRecord{
			ConsentID: c.ID, EntityCode: e.Code, Key: key, WindowEndsAt: rev.NoticeDeadlineAt, NextAttemptAt: now,
		})
	}
	_, created, err := s.revocations.Record(ctx, rev, notices)
	switch {
	case errors.Is(err, domain.ErrAlreadyRevoked), errors.Is(err, domain.ErrIdempotencyKeyReused):
		return domain.ConsentView{}, false, err
	case err != nil:
		// Includes a foreign_key_violation: the consent was read above, so a
		// missing reference is a defect, never a not found (decision #89).
		return domain.ConsentView{}, false, fmt.Errorf("record the revocation: %w", err)
	}
	if created && s.wake != nil {
		s.wake()
	}
	view, err = s.view(ctx, c, s.clock.Now().UTC())
	if err != nil {
		return domain.ConsentView{}, false, err
	}
	return view, created, nil
}

// sameRequest tells whether the revocation of the consent was recorded by the
// request with that id.
func (s *RevocationService) sameRequest(ctx context.Context, consentID, requestID string) bool {
	rev, err := s.revocations.GetByConsent(ctx, consentID)
	return err == nil && rev.RequestID == requestID
}

// Receipt renders the receipt of the revocation of a consent of the holder
// (criterion 4) in the language given and returns it with its receipt number.
// A consent of another holder, unknown or not revoked is
// ports.ErrConsentNotFound.
func (s *RevocationService) Receipt(ctx context.Context, document, consentID, lang string) ([]byte, string, error) {
	c, err := s.holderConsent(ctx, document, consentID)
	if err != nil {
		return nil, "", err
	}
	rev, err := s.revocations.GetByConsent(ctx, c.ID)
	if errors.Is(err, ports.ErrRevocationNotFound) {
		return nil, "", ports.ErrConsentNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the revocation: %w", err)
	}
	pdf, err := s.receipts.Render(rev, c, lang)
	if err != nil {
		return nil, "", fmt.Errorf("render the receipt: %w", err)
	}
	return pdf, rev.ReceiptNumber, nil
}

// RetryPending starts a new window for the entities of the consent that did
// not confirm its revocation, and only for them (decision D-8 a): a confirmed
// entity is never notified again. It returns the entities retried. The
// compliance portal exposes it (HU-W4.2-02).
func (s *RevocationService) RetryPending(ctx context.Context, consentID string, actor domain.Actor) ([]string, error) {
	if _, err := s.consents.Get(ctx, consentID); err != nil {
		if errors.Is(err, ports.ErrConsentNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("read the consent: %w", err)
	}
	resumed, err := s.notices.Resume(ctx, consentID, nil, s.clock.Now().UTC(), s.deadline, actor)
	if err != nil {
		return nil, fmt.Errorf("retry the notices: %w", err)
	}
	if len(resumed) > 0 && s.wake != nil {
		s.wake()
	}
	return resumed, nil
}

// holderConsent returns the consent if it belongs to the holder, and
// ports.ErrConsentNotFound otherwise (criterion 6 of HU-M1.3-01).
func (s *RevocationService) holderConsent(ctx context.Context, document, consentID string) (domain.Consent, error) {
	c, err := s.consents.Get(ctx, consentID)
	if errors.Is(err, ports.ErrConsentNotFound) {
		return domain.Consent{}, err
	}
	if err != nil {
		return domain.Consent{}, fmt.Errorf("read the consent: %w", err)
	}
	if domain.NormalizeDocument(c.Holder.DocumentID) != domain.NormalizeDocument(document) {
		return domain.Consent{}, ports.ErrConsentNotFound
	}
	return c, nil
}

func (s *RevocationService) view(ctx context.Context, c domain.Consent, now time.Time) (domain.ConsentView, error) {
	byConsent, err := s.events.ListByConsents(ctx, []string{c.ID})
	if err != nil {
		return domain.ConsentView{}, fmt.Errorf("read the history of the consent: %w", err)
	}
	return domain.View(c, byConsent[c.ID], now, s.names), nil
}

func newRevocationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate the revocation id: %w", err)
	}
	return RevocationIDPrefix + hex.EncodeToString(b[:]), nil
}
