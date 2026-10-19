package security_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
)

// Wiring in cmd/<service>/main.go: build the verifier once at startup (one
// JWKS download) and mount Authenticate + RequireRole only on human routes.
func ExampleAuthenticate() {
	cfg, err := security.ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("invalid auth configuration", "error", err)
		return
	}
	verifier, err := security.NewJWKSVerifier(context.Background(), cfg)
	if err != nil {
		slog.Error("could not load the user pool keys", "error", err)
		return
	}

	createQuote := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	mux := http.NewServeMux()
	mux.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("POST /v1/quotes",
		security.Authenticate(verifier)(security.RequireRole(security.RoleAdvisor)(createQuote)))
}

// Handler tests inject the caller with WithClaims instead of a real token.
func ExampleRequireRole() {
	createQuote := security.RequireRole(security.RoleAdvisor)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))

	for _, role := range []security.Role{security.RoleAdvisor, security.RoleInsured} {
		req := httptest.NewRequest(http.MethodPost, "/v1/quotes", nil)
		req = req.WithContext(security.WithClaims(req.Context(), security.Claims{
			Subject: "user-1",
			Roles:   []security.Role{role},
		}))
		rec := httptest.NewRecorder()
		createQuote.ServeHTTP(rec, req)
		fmt.Println(role, rec.Code)
	}
	// Output:
	// advisor 201
	// insured 403
}

// A use case checks ownership before running the operation; the HTTP adapter
// translates ErrForbidden with WriteForbidden.
func ExampleRequireOwnerOrRole() {
	getPolicy := func(ctx context.Context, policyOwner string) error {
		if err := security.RequireOwnerOrRole(ctx, policyOwner, security.RoleOperations); err != nil {
			return err
		}
		return nil // read and return the policy
	}

	owner := security.WithClaims(context.Background(), security.Claims{Subject: "insured-1", Roles: []security.Role{security.RoleInsured}})
	other := security.WithClaims(context.Background(), security.Claims{Subject: "insured-2", Roles: []security.Role{security.RoleInsured}})
	analyst := security.WithClaims(context.Background(), security.Claims{Subject: "ops-1", Roles: []security.Role{security.RoleOperations}})

	fmt.Println(getPolicy(owner, "insured-1") == nil)
	fmt.Println(errors.Is(getPolicy(other, "insured-1"), security.ErrForbidden))
	fmt.Println(getPolicy(analyst, "insured-1") == nil)
	// Output:
	// true
	// true
	// true
}
