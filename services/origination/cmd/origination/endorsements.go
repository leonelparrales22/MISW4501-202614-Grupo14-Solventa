package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
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
}

// loadEndorsementConfig reads PARTNER_DISPLAY_NAMES; an invalid catalog stops
// the service.
func loadEndorsementConfig(getenv func(string) string, cfg *endorsementConfig) error {
	names, err := parsePartnerNames(getenv(envPartnerDisplayNames))
	if err != nil {
		return err
	}
	cfg.partnerNames = names
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
// database: the policies, the requests and the origin of each policy (its
// charge and its offer), to know the lender.
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
		PartnerNames: cfg.partnerNames, Clock: systemClock{},
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
