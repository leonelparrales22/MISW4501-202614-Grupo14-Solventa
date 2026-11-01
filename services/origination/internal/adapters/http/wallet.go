package httpadapter

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// WalletService is the use case behind the list of the insured's policies
// (HU-M1.2-01).
type WalletService interface {
	List(ctx context.Context) (wallet.Wallet, error)
}

// WithPolicyWallet mounts GET /v1/policies: every policy of the insured, the
// ones origination issued with their renewal state (HU-W1.5-02, screen A1)
// and the ones of the policy core, with the insured value of each coverage
// and the main insured (HU-M1.2-01, screen M4 of the mobile app). It is a
// human route: the insured only sees their own. Register it after
// WithObservability.
func WithPolicyWallet(svc WalletService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/policies", listWallet(svc))
		})
	}
}

// policyHolderBody is the main insured: who they are, never their contact.
type policyHolderBody struct {
	FullName     string  `json:"full_name"`
	DocumentType *string `json:"document_type"`
	DocumentID   *string `json:"document_id"`
}

type walletItemBody struct {
	PolicyID      string               `json:"policy_id"`
	Source        string               `json:"source"`
	Line          string               `json:"line"`
	Product       *string              `json:"product"`
	InsuredObject *string              `json:"insured_object"`
	Status        string               `json:"status"`
	Holder        string               `json:"holder_name"`
	MainInsured   policyHolderBody     `json:"holder"`
	Premium       *float64             `json:"premium"`
	Currency      string               `json:"currency"`
	InsuredAmount float64              `json:"insured_amount"`
	Coverages     []policyCoverageBody `json:"coverages"`
	CoverageStart time.Time            `json:"coverage_start"`
	CoverageEnd   time.Time            `json:"coverage_end"`
	RenewalOf     *string              `json:"renewal_of"`
	// The renewal state, only for a policy origination issued: the window
	// is null and can_renew false for the others.
	RenewalWindow *windowBody         `json:"renewal_window"`
	CanRenew      bool                `json:"can_renew"`
	Renewal       *renewalSummaryBody `json:"renewal"`
}

type walletResponse struct {
	Items []walletItemBody `json:"items"`
	// Partial is true when the policy core did not answer and its policies
	// are missing: a client keeps what it had instead of taking them as gone.
	Partial bool `json:"partial"`
}

func toWalletItem(it wallet.Item) walletItemBody {
	p := it.Policy
	out := walletItemBody{
		PolicyID: p.ID, Source: string(p.Source), Line: string(p.Line), Product: optionalString(p.Product),
		InsuredObject: optionalString(p.InsuredObject), Status: string(p.Status), Holder: p.Holder.FullName,
		MainInsured: policyHolderBody{FullName: p.Holder.FullName, DocumentType: optionalString(p.Holder.DocumentType),
			DocumentID: optionalString(p.Holder.DocumentID)},
		Premium: p.Premium, Currency: p.Currency, InsuredAmount: p.InsuredAmount,
		Coverages: toCoverageBodies(p.Coverages), CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC(),
		RenewalOf: optionalString(p.RenewalOf),
	}
	if v := it.Renewal; v != nil {
		out.RenewalWindow = &windowBody{OpensAt: v.WindowOpensAt.UTC(), ClosesAt: v.WindowClosesAt.UTC()}
		out.CanRenew = v.CanRenew
		if v.Renewal != nil {
			s := toRenewalSummary(*v.Renewal)
			out.Renewal = &s
		}
	}
	return out
}

func listWallet(svc WalletService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		got, err := svc.List(req.Context())
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		out := walletResponse{Items: make([]walletItemBody, 0, len(got.Items)), Partial: got.Partial}
		for _, it := range got.Items {
			out.Items = append(out.Items, toWalletItem(it))
		}
		writeJSON(w, http.StatusOK, out)
	}
}
