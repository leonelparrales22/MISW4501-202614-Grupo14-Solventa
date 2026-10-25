package httpadapter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// issuanceBody is one row of the monitor (HU-W1.3-04).
type issuanceBody struct {
	AcceptanceID  string     `json:"acceptance_id"`
	OfferID       string     `json:"offer_id"`
	CustomerID    string     `json:"customer_id"`
	CustomerName  *string    `json:"customer_name"`
	Channel       string     `json:"channel"`
	Status        string     `json:"status"`
	Step          string     `json:"step"`
	AcceptedAt    time.Time  `json:"accepted_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Amount        *float64   `json:"amount"`
	Currency      *string    `json:"currency"`
	Attempts      int        `json:"attempts"`
	PolicyID      *string    `json:"policy_id"`
	Reason        *string    `json:"reason"`
	FailedAt      *time.Time `json:"failed_at"`
	NotifiedAt    *time.Time `json:"notified_at"`
	Unattended    bool       `json:"unattended"`
	ResumeCount   int        `json:"resume_count"`
	LastResumedAt *time.Time `json:"last_resumed_at"`
	LastResumedBy *string    `json:"last_resumed_by"`
}

type issuanceCountsBody struct {
	Accepted            int `json:"accepted"`
	Charging            int `json:"charging"`
	ChargingWithRetries int `json:"charging_with_retries"`
	ActiveToday         int `json:"active_today"`
	NotifiedToday       int `json:"notified_today"`
	Exceptions          int `json:"exceptions"`
	Unattended          int `json:"unattended"`
}

type issuancesResponse struct {
	Items  []issuanceBody     `json:"items"`
	Counts issuanceCountsBody `json:"counts"`
}

type issuanceActionBody struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type issuanceActionsResponse struct {
	Items []issuanceActionBody `json:"items"`
}

type retryResponse struct {
	AcceptanceID string `json:"acceptance_id"`
	Action       string `json:"action"`
}

func listIssuances(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f, dayFrom, msg := issuanceFilterOf(req)
		if msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		overview, err := svc.Overview(req.Context(), f, dayFrom)
		switch {
		case err == nil:
		case errors.Is(err, billing.ErrInvalidLimit), errors.Is(err, billing.ErrInvalidFilter):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		case isAuthError(err):
			writeAuthError(w, err)
			return
		default:
			slog.ErrorContext(req.Context(), "could not list the issuances", "error", err)
			writeError(w, http.StatusServiceUnavailable, "charge_unavailable", "the issuances could not be read; retry later")
			return
		}
		c := overview.Counts
		out := issuancesResponse{
			Items: make([]issuanceBody, 0, len(overview.Items)),
			Counts: issuanceCountsBody{
				Accepted: c.Accepted, Charging: c.Charging, ChargingWithRetries: c.ChargingWithRetries,
				ActiveToday: c.ActiveToday, NotifiedToday: c.NotifiedToday, Exceptions: c.Exceptions, Unattended: c.Unattended,
			},
		}
		for _, i := range overview.Items {
			out.Items = append(out.Items, toIssuanceBody(i))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func listIssuanceActions(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		items, err := svc.ListActions(req.Context(), id)
		switch {
		case err == nil:
		case isAuthError(err):
			writeAuthError(w, err)
			return
		default:
			slog.ErrorContext(req.Context(), "could not list the issuance actions", "error", err)
			writeError(w, http.StatusServiceUnavailable, "charge_unavailable", "the actions could not be read; retry later")
			return
		}
		out := issuanceActionsResponse{Items: make([]issuanceActionBody, 0, len(items))}
		for _, a := range items {
			out.Items = append(out.Items, issuanceActionBody{
				ID: a.ID, Action: string(a.Action), Actor: a.Actor, Note: a.Note, CreatedAt: a.CreatedAt.UTC(),
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func retryIssuance(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		var in resumeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		action, err := svc.RetryIssuance(req.Context(), id, in.Note)
		switch {
		case err == nil:
			writeJSON(w, http.StatusAccepted, retryResponse{AcceptanceID: id, Action: string(action)})
		case errors.Is(err, billing.ErrInvalidNote):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, billing.ErrIssuanceNotFound):
			writeError(w, http.StatusNotFound, "issuance_not_found",
				"the acceptance has no premium charge or contractual issuance to retry")
		case errors.Is(err, billing.ErrIssuanceNotRetryable):
			writeError(w, http.StatusConflict, "issuance_not_retryable",
				"nothing of the issuance is failed: another analyst or the automatic process may have retried it already")
		case isAuthError(err):
			writeAuthError(w, err)
		default:
			slog.ErrorContext(req.Context(), "could not retry the issuance", "error", err)
			writeError(w, http.StatusServiceUnavailable, "charge_unavailable", "the issuance could not be retried; retry later")
		}
	}
}

// issuanceFilterOf reads status, channel, from, to (RFC 3339), day_from (the
// start of the day of the caller, for the counts of today) and limit from the
// query. A non-empty message is the reason the request is invalid.
func issuanceFilterOf(req *http.Request) (f domain.IssuanceFilter, dayFrom time.Time, msg string) {
	q := req.URL.Query()
	f = domain.IssuanceFilter{Status: domain.IssuanceStatus(q.Get("status")), Channel: domain.Channel(q.Get("channel"))}
	if f.Status != "" && !f.Status.Valid() {
		return f, dayFrom, "status must be one of accepted, charging, active, notified or exception"
	}
	if f.Channel != "" && f.Channel != domain.ChannelWeb && f.Channel != domain.ChannelMobile && f.Channel != domain.ChannelAdvisor {
		return f, dayFrom, "channel must be web or mobile"
	}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To, "day_from": &dayFrom} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return f, dayFrom, name + " must be an RFC 3339 date-time"
			}
			*dst = t.UTC()
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return f, dayFrom, "to must not be before from"
	}
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > billing.MaxExceptionsLimit {
			return f, dayFrom, "limit must be an integer between 1 and 200"
		}
		f.Limit = v
	}
	if dayFrom.IsZero() {
		now := time.Now().UTC()
		dayFrom = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	}
	return f, dayFrom, ""
}

func toIssuanceBody(i domain.Issuance) issuanceBody {
	b := issuanceBody{
		AcceptanceID: i.AcceptanceID, OfferID: i.OfferID, CustomerID: i.CustomerID, CustomerName: optionalString(i.CustomerName),
		Channel: string(i.Channel), Status: string(i.Status), Step: string(i.Step),
		AcceptedAt: i.AcceptedAt.UTC(), UpdatedAt: i.UpdatedAt.UTC(), Attempts: i.Attempts,
		PolicyID: optionalString(i.PolicyID), NotifiedAt: optionalTime(i.NotifiedAt), Unattended: i.Unattended,
		ResumeCount: i.ResumeCount, LastResumedAt: optionalTime(i.LastResumedAt), LastResumedBy: optionalString(i.LastResumedBy),
	}
	if i.Status != domain.IssuanceAccepted {
		amount := domain.FromMinorUnits(i.AmountMinor)
		b.Amount, b.Currency = &amount, optionalString(i.Currency)
	}
	if i.Status == domain.IssuanceException {
		b.Reason, b.FailedAt = optionalString(string(i.FailureReason)), optionalTime(i.FailedAt)
	}
	return b
}
