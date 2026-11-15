package httpadapter

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// internalPolicyIDPattern accepts the policy ids of origination ("pol-" and the
// acceptance id) and of the policy core.
var internalPolicyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type openClaimsResponse struct {
	PolicyID   string `json:"policy_id"`
	OpenClaims int    `json:"open_claims"`
}

// WithOpenClaims mounts GET /internal/policies/{policy_id}/open-claims
// (HU-W1.4-02, decision D-6 a): how many claims of a policy are in progress,
// for origination to check before it applies an endorsement. It carries no
// token: it lives outside /v1/claims, so neither the API Gateway nor the load
// balancer route it, and only the services inside the VPC reach it through
// Cloud Map, like GET /v1/audit/consent-verifications of audit. It names no
// claim and nobody who filed one: only the count.
func WithOpenClaims(reader ports.OpenClaimsReader) Option {
	return func(r chi.Router) {
		r.With(noStore).Get("/internal/policies/{policy_id}/open-claims", func(w http.ResponseWriter, req *http.Request) {
			policyID := chi.URLParam(req, "policy_id")
			if !internalPolicyIDPattern.MatchString(policyID) {
				writeError(w, http.StatusBadRequest, "invalid_policy_id", "the policy id is not valid")
				return
			}
			n, err := reader.OpenClaims(req.Context(), policyID)
			if err != nil {
				slog.ErrorContext(req.Context(), "could not count the open claims of a policy", "policy_id", policyID, "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable", "the claims cannot be counted now")
				return
			}
			writeJSON(w, http.StatusOK, openClaimsResponse{PolicyID: policyID, OpenClaims: n})
		})
	}
}
