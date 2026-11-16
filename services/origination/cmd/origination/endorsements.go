package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	claimsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/claims"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// envPartnerDisplayNames is the JSON catalog of the display names of the
// partners, by code (decision D-14 a of HU-W1.4-01): the onerous beneficiary
// of a policy is shown by the name of the lender. It has the format of the
// variable of the same name of audit, and Terraform feeds both from the same
// value.
const envPartnerDisplayNames = "PARTNER_DISPLAY_NAMES"

var partnerCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// endorsementConfig configures the change requests of the insured
// (HU-W1.4-01).
type endorsementConfig struct {
	// partnerNames are the display names of the partners; empty shows the
	// code of the partner.
	partnerNames map[string]string
	// claimsURL is the claims service inside the VPC (CLAIMS_URL, HU-W1.4-02):
	// the claims in progress of a policy. Empty: that validation is
	// unverifiable and no endorsement of beneficiaries nor of insured value
	// is applied.
	claimsURL     string
	claimsTimeout time.Duration
	// kycURL is the KYC provider (KYC_URL): the documents of the new
	// beneficiaries in the registry. Empty: that validation is unverifiable
	// and no endorsement of beneficiaries is applied.
	kycURL               string
	documentCheckTimeout time.Duration
}

// Variables of the review of the endorsements (HU-W1.4-02).
const (
	envClaimsURL            = "CLAIMS_URL"
	envClaimsTimeout        = "CLAIMS_TIMEOUT_MS"
	envDocumentCheckTimeout = "DOCUMENT_CHECK_TIMEOUT_MS"
)

// loadEndorsementConfig reads PARTNER_DISPLAY_NAMES (an invalid catalog stops
// the service), and CLAIMS_URL, CLAIMS_TIMEOUT_MS (300 ms by default), KYC_URL
// and DOCUMENT_CHECK_TIMEOUT_MS (2000 ms by default) for the validations of
// the endorsements.
func loadEndorsementConfig(getenv func(string) string, cfg *endorsementConfig) error {
	names, err := parsePartnerNames(getenv(envPartnerDisplayNames))
	if err != nil {
		return err
	}
	cfg.partnerNames = names
	cfg.claimsURL, cfg.kycURL = getenv(envClaimsURL), getenv(envKYCURL)
	ms, err := intVar(getenv, envClaimsTimeout, int(claimsadapter.DefaultTimeout/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.claimsTimeout = time.Duration(ms) * time.Millisecond
	if ms, err = intVar(getenv, envDocumentCheckTimeout, int(kyc.DocumentCheckTimeout/time.Millisecond)); err != nil {
		return err
	}
	cfg.documentCheckTimeout = time.Duration(ms) * time.Millisecond
	return nil
}

// parsePartnerNames reads the catalog of display names; empty is an empty
// catalog.
func parsePartnerNames(raw string) (map[string]string, error) {
	names := map[string]string{}
	if raw == "" {
		return names, nil
	}
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of partner code to display name: %w", envPartnerDisplayNames, err)
	}
	for code, name := range names {
		if !partnerCodePattern.MatchString(code) || name == "" {
			return nil, fmt.Errorf("%s has an invalid entry for %q: codes are letters, digits, '-' or '_' and names are not empty",
				envPartnerDisplayNames, code)
		}
	}
	return names, nil
}

// newEndorsementService wires the change requests over origination's
// database: the policies, the requests, the origin of each policy (its
// charge and its offer), to know the lender, and its cancellation.
func newEndorsementService(cfg endorsementConfig, db *sql.DB, offers ports.OfferReader) (*endorsements.Service, error) {
	if db == nil {
		return nil, fmt.Errorf("the change requests need origination's database (%s)", envDatabaseURL)
	}
	customers := postgres.NewCustomerStore(db)
	policies := postgres.NewPolicyStore(db)
	return endorsements.NewService(endorsements.Config{
		Policies: policies, Requests: postgres.NewChangeRequestStore(db),
		Origins: renewal.OriginFinder{Policies: policies, Charges: postgres.NewChargeStore(db), Offers: offers,
			BySubject: customers, ByDocument: customers},
		PartnerNames: cfg.partnerNames,
		// HU-W1.4-02: the terms the insured sees show the last endorsement.
		Endorsements: postgres.NewEndorsementStore(db),
		Clock:        systemClock{},
		// HU-W1.6-02: a policy cancelled for non-payment is not in force, as
		// for the renewal (adjustment A-9).
		Cancellations: postgres.NewInstallmentStore(db),
	})
}

// endorsementOptions mounts the change requests of the insured (HU-W1.4-01)
// when the premium charge is on: the policies that can change are the ones
// its charge activated. Without the charge there is nothing to change and
// nothing is mounted.
func endorsementOptions(billing bool, cfg endorsementConfig, db *sql.DB, offers ports.OfferReader,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	if !billing {
		return nil, nil
	}
	svc, err := newEndorsementService(cfg, db, offers)
	if err != nil {
		return nil, err
	}
	log.Printf("change requests (HU-W1.4-01): /v1/policies/{policy_id}/current-terms, /v1/policies/{policy_id}/change-requests and /v1/policies/change-requests for the insured, %d partner display names",
		len(cfg.partnerNames))
	return []httpadapter.Option{httpadapter.WithChangeRequests(svc, verifier)}, nil
}

// endorsementReviewOptions mounts the review of the change requests by the
// operations analyst (HU-W1.4-02) where the change requests are: with the
// premium charge and over the database of origination. It asks claims
// (CLAIMS_URL) for the claims in progress and KYC (KYC_URL, a client of its
// own) for the documents of the new beneficiaries; without them those
// validations are unverifiable and nothing that needs them is applied (fail
// closed). The e-mail of each decision goes through the shared e-mail client;
// without it the e-mails wait until EMAIL_URL is set.
func endorsementReviewOptions(ctx context.Context, billing bool, cfg endorsementConfig, db *sql.DB, offers ports.OfferReader,
	sender emailadapter.Sender, verifier security.Verifier) ([]httpadapter.Option, error) {
	if !billing {
		return nil, nil
	}
	if db == nil {
		return nil, fmt.Errorf("the review of the endorsements needs the database of origination (%s)", envDatabaseURL)
	}
	customers := postgres.NewCustomerStore(db)
	policies := postgres.NewPolicyStore(db)
	store := postgres.NewEndorsementStore(db)
	origins := renewal.OriginFinder{Policies: policies, Charges: postgres.NewChargeStore(db), Offers: offers,
		BySubject: customers, ByDocument: customers}
	review := endorsements.ReviewConfig{
		Policies: policies, Requests: postgres.NewChangeRequestStore(db), Store: store,
		Terms:    endorsements.NewTerms(origins, cfg.partnerNames).WithEndorsements(store),
		Renewals: postgres.NewRenewalStore(db), Pricer: endorsements.Pricer{Origins: origins, Offers: offers, Rater: rating.New()},
		Clock: systemClock{},
	}
	// EC-D01: hard cuts and a circuit breaker of 3 failures, 5 s and 3
	// successes for each new dependency.
	if cfg.claimsURL != "" {
		review.Claims = claimsadapter.NewOpenClaims(claimsadapter.Config{BaseURL: cfg.claimsURL, Timeout: cfg.claimsTimeout,
			Breaker: resilience.DefaultConfig()})
	} else {
		log.Printf("%s is not set: the claims in progress are unverifiable, so no endorsement of beneficiaries nor of insured value is applied (HU-W1.4-02)",
			envClaimsURL)
	}
	if cfg.kycURL != "" {
		review.Documents = kycadapter.NewDocumentChecker(kyc.NewClient(kyc.Config{BaseURL: cfg.kycURL,
			Timeout: cfg.documentCheckTimeout, Breaker: resilience.DefaultConfig()}))
	} else {
		log.Printf("%s is not set: the documents of the new beneficiaries are unverifiable, so no endorsement of beneficiaries is applied (HU-W1.4-02)",
			envKYCURL)
	}
	if sender != nil {
		worker := endorsements.NewNoticeWorker(store, emailadapter.NewEndorsementNotifier(sender), systemClock{},
			endorsements.NoticeConfig{})
		go worker.Run(ctx)
		review.Wake = worker.Notify
	} else {
		log.Printf("%s is not set: the e-mails of the decisions on the change requests wait until it is (HU-W1.4-02)", envEmailURL)
	}
	svc, err := endorsements.NewReview(review)
	if err != nil {
		return nil, err
	}
	log.Printf("endorsements (HU-W1.4-02): /v1/policies/endorsement-requests and /v1/policies/{policy_id}/endorsements for operations; claims %q (%s), KYC %q (%s)",
		cfg.claimsURL, cfg.claimsTimeout, cfg.kycURL, cfg.documentCheckTimeout)
	return []httpadapter.Option{httpadapter.WithEndorsementReview(svc, verifier)}, nil
}
