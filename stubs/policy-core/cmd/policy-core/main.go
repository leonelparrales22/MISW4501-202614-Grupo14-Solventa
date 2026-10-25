// Command policy-core is the stub of the policy core: the system of record of
// the insured's policies (auto, home, travel) that the claims service asks
// before a claim is filed (HU-W2.1-01). Faults and latency are injected from
// outside with Toxiproxy; ARTIFICIAL_DELAY_MS is a shortcut for quick local
// runs.
//
// Every holder gets a deterministic set of policies derived from their id, so
// any Cognito subject works without seeding. POLICY_SEED_FILE replaces the
// policies of chosen holders, which is how the tests fix the boundary values.
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxHolderIDLength bounds the holder id: a Cognito subject is a UUID.
const maxHolderIDLength = 128

// Lines and statuses of a policy, as the core names them.
const (
	lineAuto   = "auto"
	lineHome   = "home"
	lineTravel = "travel"

	statusActive  = "active"
	statusExpired = "expired"
)

type policy struct {
	PolicyID         string    `json:"policy_id"`
	Line             string    `json:"line"`
	Product          string    `json:"product"`
	InsuredObject    string    `json:"insured_object"`
	Status           string    `json:"status"`
	ValidFrom        time.Time `json:"valid_from"`
	ValidTo          time.Time `json:"valid_to"`
	CoveredIncidents []string  `json:"covered_incidents"`
}

type policiesResponse struct {
	Policies []policy `json:"policies"`
}

// seedFile is the shape of POLICY_SEED_FILE: the policies of each holder it
// names. An empty list is a holder without policies.
type seedFile struct {
	Holders map[string][]policy `json:"holders"`
}

func main() {
	delay := time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond
	seed, err := loadSeed(os.Getenv("POLICY_SEED_FILE"))
	if err != nil {
		log.Fatalf("could not load POLICY_SEED_FILE: %v", err)
	}

	addr := ":" + orDefault(os.Getenv("PORT"), "9060")
	log.Printf("policy-core stub listening on %s (delay=%s, seeded holders=%d)", addr, delay, len(seed))
	srv := &http.Server{Addr: addr, Handler: newHandler(delay, seed, time.Now), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func newHandler(delay time.Duration, seed map[string][]policy, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /holders/{holderID}/policies", func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		id := strings.TrimSpace(r.PathValue("holderID"))
		if id == "" || len(id) > maxHolderIDLength {
			http.Error(w, "holder id is required (up to 128 characters)", http.StatusBadRequest)
			return
		}
		policies, seeded := seed[id]
		if !seeded {
			policies = buildPolicies(id, now())
		}
		if policies == nil {
			policies = []policy{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(policiesResponse{Policies: policies}); err != nil {
			log.Printf("could not write the response: %v", err)
		}
	})
	return mux
}

// buildPolicies derives the policies of a holder from their id: an active
// auto policy, an active home policy, an active travel policy (parametric, so
// it covers no type that is filed) and an auto policy that expired 30 days
// ago. The auto policy does not cover glass when the hash is even, so both
// answers of the coverage check can be reached. The validity is anchored to
// the current UTC day, so it stays the same all day long.
func buildPolicies(id string, now time.Time) []policy {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	sum := h.Sum32()

	today := now.UTC().Truncate(24 * time.Hour)
	// The active policies started between 30 and 329 days ago and last a
	// year, so they are active for 36 days at least.
	from := today.AddDate(0, 0, -30-int(sum%300))
	to := from.AddDate(1, 0, 0)
	expiredTo := today.AddDate(0, 0, -30)
	expiredFrom := expiredTo.AddDate(-1, 0, 0)

	autoCovers := []string{"collision", "theft", "third_party_damage", "glass", "other"}
	if sum%2 == 0 {
		autoCovers = []string{"collision", "theft", "third_party_damage", "other"}
	}

	return []policy{
		{
			PolicyID: policyID("AU", from, sum), Line: lineAuto, Product: "Autos", InsuredObject: plate(sum),
			Status: statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: autoCovers,
		},
		{
			PolicyID: policyID("HG", from, sum/7), Line: lineHome, Product: "Hogar",
			InsuredObject: fmt.Sprintf("Calle %d # %d-%d, Bogotá", 10+sum%150, 1+sum%90, 1+sum%60),
			Status:        statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: []string{"water_damage"},
		},
		{
			PolicyID: policyID("VJ", from, sum/11), Line: lineTravel, Product: "Viaje", InsuredObject: "Viajes nacionales",
			Status: statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: []string{"flight_delay"},
		},
		{
			PolicyID: policyID("AU", expiredFrom, sum/13), Line: lineAuto, Product: "Autos", InsuredObject: plate(sum / 3),
			Status: statusExpired, ValidFrom: expiredFrom, ValidTo: expiredTo, CoveredIncidents: autoCovers,
		},
	}
}

// policyID builds an id like AU-2025-118204: the line prefix, the year the
// policy started and six digits.
func policyID(prefix string, from time.Time, n uint32) string {
	return fmt.Sprintf("%s-%d-%06d", prefix, from.Year(), n%1_000_000)
}

// plate builds a Colombian car plate: three letters and three digits.
func plate(n uint32) string {
	letters := []byte{byte('A' + n%26), byte('A' + n/26%26), byte('A' + n/676%26)}
	return fmt.Sprintf("%s-%03d", letters, n%1000)
}

func loadSeed(path string) (map[string][]policy, error) {
	if path == "" {
		return map[string][]policy{}, nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var seed seedFile
	if err := json.Unmarshal(raw, &seed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if seed.Holders == nil {
		return map[string][]policy{}, nil
	}
	return seed.Holders, nil
}

func envInt(name string) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
