// Package fcm is the adapter to Firebase Cloud Messaging, the push provider of
// the architecture (HU-M3.3-01, replacing the stubs/push simulator in AWS). It
// sends through the HTTP v1 API with the OAuth 2 token of a service account,
// enforces a hard per-call timeout and protects the caller with a circuit
// breaker, and it classifies every answer with the errors of pkg/idempotency,
// like pkg/adapters/push.
//
// FCM has no idempotency key: a retry after an ambiguous answer may deliver
// the message twice. The messages are data only and the app shows each
// notification once (it marks it as already shown), so a duplicate reaches
// the device but not the user.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// Scope is the OAuth 2 scope of the FCM HTTP v1 API.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

// DefaultBaseURL is the FCM HTTP v1 API.
const DefaultBaseURL = "https://fcm.googleapis.com"

// DefaultTimeout is the hard cut of one send, as for pkg/adapters/push.
const DefaultTimeout = 2 * time.Second

// DefaultTTL is how long FCM keeps a message for a device that is offline:
// the 24 h the notifications service retries a push.
const DefaultTTL = 24 * time.Hour

const maxBodyBytes = 1 << 16

// Errors of a send. Each wraps the matching error of pkg/idempotency.
var (
	// ErrRejected means FCM answered and refused the message: the token is no
	// longer registered (404 UNREGISTERED) or the message is invalid (400).
	// Retrying gets the same answer.
	ErrRejected = fmt.Errorf("fcm rejected the message: %w", idempotency.ErrRejected)
	// ErrAmbiguous means the outcome is unknown: timeout, connection error,
	// 429, 5xx or an authentication FCM refused. A retry may deliver it twice.
	ErrAmbiguous = fmt.Errorf("fcm outcome unknown: %w", idempotency.ErrAmbiguous)
	// ErrUnavailable means the call was not sent: the circuit breaker is
	// isolating FCM, or there was no access token to send it with.
	ErrUnavailable = fmt.Errorf("fcm call not sent: %w", idempotency.ErrNotSent)
)

// ErrInvalidMessage is returned for a message without token or data. It is a
// caller error: it never reaches FCM nor counts against the circuit breaker.
var ErrInvalidMessage = errors.New("token and data are required")

// Message is one data message to one device.
type Message struct {
	// Token is the registration token the app got from Firebase.
	Token string
	// Data is what the app receives: it writes and shows the notification
	// itself, in its language, with its channels.
	Data map[string]string
	// CollapseKey, when set, keeps only the last undelivered message with the
	// same key for an offline device.
	CollapseKey string
	// TTL is how long FCM keeps the message for an offline device; zero takes
	// DefaultTTL.
	TTL time.Duration
}

// Receipt is FCM's record of an accepted message.
type Receipt struct {
	// ID is the name FCM gives the message: projects/<id>/messages/<id>.
	ID string
}

// Config configures the client.
type Config struct {
	// ProjectID is the Firebase project (solventa-94126).
	ProjectID string
	// Credentials is the JSON of the service account. Ignored when
	// TokenSource is set.
	Credentials []byte
	// TokenSource, when set, gives the access tokens (tests).
	TokenSource oauth2.TokenSource
	// BaseURL defaults to DefaultBaseURL.
	BaseURL string
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient, when set, carries the calls; Timeout still bounds every call.
	HTTPClient *http.Client
}

// Client sends data messages through FCM behind a circuit breaker.
type Client struct {
	http    *http.Client
	tokens  oauth2.TokenSource
	url     string
	timeout time.Duration
	breaker *resilience.Breaker
}

// NewClient builds a client. It fails when the project is missing or the
// credentials are not the JSON of a service account.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, errors.New("fcm: the project id is required")
	}
	tokens := cfg.TokenSource
	if tokens == nil {
		jwt, err := google.JWTConfigFromJSON(cfg.Credentials, Scope)
		if err != nil {
			return nil, fmt.Errorf("fcm: read the service account: %w", err)
		}
		tokens = oauth2.ReuseTokenSource(nil, jwt.TokenSource(context.Background()))
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		http: httpClient, tokens: tokens, timeout: timeout,
		url:     base + "/v1/projects/" + cfg.ProjectID + "/messages:send",
		breaker: resilience.NewBreaker("fcm", cfg.Breaker),
	}, nil
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string { return c.breaker.State() }

type answer struct {
	receipt  Receipt
	rejected error
}

// Send delivers the message. It returns the receipt on success, ErrRejected
// when FCM refuses it, ErrAmbiguous when the outcome is unknown and
// ErrUnavailable when the call was not sent.
func (c *Client) Send(ctx context.Context, m Message) (Receipt, error) {
	if m.Token == "" || len(m.Data) == 0 {
		return Receipt{}, ErrInvalidMessage
	}
	token, err := c.tokens.Token()
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: get the access token: %w", ErrUnavailable, err)
	}
	ans, err := resilience.Execute(c.breaker, func() (answer, error) { return c.send(ctx, token.AccessToken, m) })
	switch {
	case errors.Is(err, resilience.ErrOpen):
		return Receipt{}, ErrUnavailable
	case err != nil:
		return Receipt{}, fmt.Errorf("%w: %w", ErrAmbiguous, err)
	case ans.rejected != nil:
		return Receipt{}, ans.rejected
	}
	return ans.receipt, nil
}

// request is the body of messages:send.
type request struct {
	Message message `json:"message"`
}

type message struct {
	Token   string            `json:"token"`
	Data    map[string]string `json:"data"`
	Android android           `json:"android"`
}

type android struct {
	// Priority HIGH wakes the app even when it is closed or the device dozes.
	Priority    string `json:"priority"`
	TTL         string `json:"ttl"`
	CollapseKey string `json:"collapse_key,omitempty"`
}

func (c *Client) send(ctx context.Context, accessToken string, m Message) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	ttl := m.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	raw, err := json.Marshal(request{Message: message{Token: m.Token, Data: m.Data, Android: android{
		Priority: "HIGH", TTL: strconv.FormatInt(int64(ttl/time.Second), 10) + "s", CollapseKey: m.CollapseKey,
	}}})
	if err != nil {
		return answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, maxBodyBytes)

	switch resp.StatusCode {
	case http.StatusOK:
		var r struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(body).Decode(&r); err != nil {
			return answer{}, fmt.Errorf("decode the answer: %w", err)
		}
		return answer{receipt: Receipt{ID: r.Name}}, nil
	case http.StatusNotFound, http.StatusBadRequest:
		// 404 UNREGISTERED: the app was uninstalled or its token changed.
		// 400 INVALID_ARGUMENT: the token or the message are not valid.
		return answer{rejected: fmt.Errorf("%w: status %d %s", ErrRejected, resp.StatusCode, status(body))}, nil
	default:
		// 401/403 (the credentials), 429 (quota) and 5xx: worth retrying.
		return answer{}, fmt.Errorf("unexpected status %d %s", resp.StatusCode, status(body))
	}
}

// status reads the status of an FCM error body: {"error":{"status":"UNREGISTERED"}}.
func status(body io.Reader) string {
	var eb struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	_ = json.NewDecoder(body).Decode(&eb)
	return eb.Error.Status
}
