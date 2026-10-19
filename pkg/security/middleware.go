package security

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// Error codes of the JSON error body. The body has the same shape as the
// services' Error schema: {"error": "<code>", "message": "<text>"}.
const (
	ErrorCodeUnauthorized = "unauthorized"
	ErrorCodeForbidden    = "forbidden"
)

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Authenticate verifies the bearer token of every request. Without an
// "Authorization: Bearer <token>" header, or with a token the verifier
// rejects, it answers 401 and does not call next. Otherwise it stores the
// verified Claims in the request context.
//
// Mount it only on human routes; GET /health and partner routes (API key at
// the API Gateway) must stay outside it.
func Authenticate(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				writeUnauthorized(w, "Bearer", "a bearer token is required")
				return
			}
			c, err := v.Verify(r.Context(), raw)
			if err != nil {
				slog.DebugContext(r.Context(), "bearer token rejected", "reason", err)
				writeUnauthorized(w, `Bearer error="invalid_token"`, "the token is invalid or expired")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), c)))
		})
	}
}

// RequireRole lets the request through only if the authenticated caller has
// one of the given roles; otherwise it answers 403 and does not call next. It
// answers 401 if Authenticate did not run before it.
func RequireRole(roles ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFromContext(r.Context())
			if !ok {
				WriteUnauthorized(w)
				return
			}
			if !c.HasAnyRole(roles...) {
				WriteForbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteUnauthorized writes the 401 response. HTTP adapters use it to translate
// ErrUnauthenticated.
func WriteUnauthorized(w http.ResponseWriter) {
	writeUnauthorized(w, "Bearer", "authentication is required")
}

// WriteForbidden writes the 403 response. HTTP adapters use it to translate
// ErrForbidden returned by a use case, so the business operation is not run.
func WriteForbidden(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, ErrorCodeForbidden, "the caller is not allowed to perform this operation")
}

func writeUnauthorized(w http.ResponseWriter, challenge, message string) {
	w.Header().Set("WWW-Authenticate", challenge)
	writeError(w, http.StatusUnauthorized, ErrorCodeUnauthorized, message)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorBody{Error: code, Message: message}); err != nil {
		slog.Error("could not write the error response", "error", err)
	}
}

// bearerToken extracts the token of an "Authorization: Bearer <token>" header.
// The scheme is case-insensitive (RFC 7235); the token must be a single
// non-empty word.
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
