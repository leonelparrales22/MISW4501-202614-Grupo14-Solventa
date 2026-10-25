package domain

import (
	"errors"
	"regexp"
	"time"
)

// policyIDPattern keeps a policy id safe to use in a storage key and a URL.
var policyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidPolicyID tells whether s can be used as a policy id.
func ValidPolicyID(s string) bool { return policyIDPattern.MatchString(s) }

// ContractSLO is how long after the coverage activation the contractual
// document must be available and the insured notified (HU-W1.3-03: 60 s).
const ContractSLO = 60 * time.Second

// CoverageActivated is the fact that the premium was charged and the coverage
// is active (HU-W1.3-02, HA-D02). It carries everything the contractual
// document shows, so generating it needs no call to another module. EventID
// makes a redelivery recognizable.
type CoverageActivated struct {
	EventID      string
	PolicyID     string
	AcceptanceID string
	// CustomerID is the insured who owns the policy: the subject of their token.
	CustomerID string
	Holder     Applicant

	Premium       float64
	Currency      string
	InsuredAmount float64
	Coverages     []Coverage

	ActivatedAt   time.Time
	CoverageStart time.Time
	CoverageEnd   time.Time

	// RenewalOf is the policy this one renews (HU-W1.5-02); empty for new
	// business. The coverage of a renewal starts when the renewed one ends.
	RenewalOf string
}

// Validate reports why the event cannot produce a contractual document.
func (e CoverageActivated) Validate() error {
	switch {
	case e.EventID == "", e.CustomerID == "":
		return errors.New("event_id and customer_id are required")
	case !ValidPolicyID(e.PolicyID):
		return errors.New("policy_id is required (letters, digits, - and _, up to 128)")
	case e.Holder.FullName == "" || e.Holder.Email == "":
		return errors.New("the holder name and email are required")
	case e.Premium <= 0 || e.Currency == "" || len(e.Coverages) == 0:
		return errors.New("premium, currency and coverages are required")
	case e.ActivatedAt.IsZero() || !e.CoverageEnd.After(e.CoverageStart):
		return errors.New("activated_at and a coverage period are required")
	}
	return nil
}

// ContractDocument is a generated contractual document, stored and tied
// permanently to the policy. Version starts at 1; a regenerated document
// would get the next version and keep the previous ones.
type ContractDocument struct {
	ID          string
	PolicyID    string
	Version     int
	StorageKey  string
	ContentType string
	SizeBytes   int
	// Checksum is the SHA-256 of the content, in hexadecimal.
	Checksum    string
	GeneratedAt time.Time
}

// NotificationStatus is the delivery state of the issuance notification.
type NotificationStatus string

const (
	NotificationPending NotificationStatus = "pending"
	NotificationSent    NotificationStatus = "sent"
	NotificationFailed  NotificationStatus = "failed"
)

// ChannelEmail is the contact channel of the insured used for the
// notification (the one configured in their data).
const ChannelEmail = "email"

// ContractStatus is how far the contractual issuance of a policy got.
type ContractStatus string

const (
	// ContractPending: the activation was received, the document is not ready.
	ContractPending ContractStatus = "pending"
	// ContractDocumentReady: the document is stored and associated; the
	// notification is still pending or being retried.
	ContractDocumentReady ContractStatus = "document_ready"
	// ContractCompleted: document available and insured notified.
	ContractCompleted ContractStatus = "completed"
	// ContractFailed: the notification could not be delivered after every
	// attempt; the document remains available to the insured.
	ContractFailed ContractStatus = "failed"
)

// ContractIssuance is the state of the contractual issuance of one policy. It
// is persisted when the activation is received, so a failure never loses it
// and a redelivered event never repeats a step that already finished.
type ContractIssuance struct {
	Event    CoverageActivated
	Status   ContractStatus
	Document *ContractDocument

	NotificationStatus  NotificationStatus
	NotificationChannel string
	NotifiedAt          time.Time

	// AvailableAt is when the document became available; Elapsed counts from
	// the activation.
	AvailableAt time.Time

	Attempts      int
	LastFailureAt time.Time
	LastFailure   string
	// NextAttemptAt is when the issuance is processed next; the zero time
	// once it is completed or failed.
	NextAttemptAt time.Time
	ReceivedAt    time.Time
	UpdatedAt     time.Time
}

// WithinSLO tells whether the document was available inside ContractSLO.
func (c ContractIssuance) WithinSLO() bool {
	return c.Document != nil && c.AvailableAt.Sub(c.Event.ActivatedAt) <= ContractSLO
}

// Audit event names of the contractual issuance.
const (
	AuditDocumentGenerated  = "document_generated"
	AuditNotificationSent   = "notification_sent"
	AuditNotificationFailed = "notification_failed"
)

// IssuanceAuditEntry is one append-only record that lets the issuance be
// rebuilt and proved before a claim or a regulatory review (HU-W1.3-03
// criterion 4): when it happened, which document version and how the delivery
// ended.
type IssuanceAuditEntry struct {
	PolicyID        string
	Event           string
	At              time.Time
	DocumentID      string
	DocumentVersion int
	Checksum        string
	// DeliveryResult is empty for document events, sent or failed for
	// notification events.
	DeliveryResult NotificationStatus
	Channel        string
	Attempt        int
	Detail         string
}

// IssuedNotification is the message that tells the insured their policy was
// issued, with a secure link to the contractual document.
type IssuedNotification struct {
	To          string
	HolderName  string
	PolicyID    string
	DocumentURL string
	// LinkExpiresAt is when DocumentURL stops working.
	LinkExpiresAt time.Time
	// IdempotencyKey lets the channel drop a message sent twice.
	IdempotencyKey string
}

// ContractData is what the contractual document shows.
type ContractData struct {
	PolicyID      string
	Version       int
	Holder        Applicant
	Premium       float64
	Currency      string
	InsuredAmount float64
	Coverages     []Coverage
	CoverageStart time.Time
	CoverageEnd   time.Time
	ActivatedAt   time.Time
	GeneratedAt   time.Time
}
