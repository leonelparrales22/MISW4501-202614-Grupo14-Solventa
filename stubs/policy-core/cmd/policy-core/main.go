// Command policy-core is the stub of the policy core: the system of record of
// the insured's policies (auto, home, travel) that the claims service asks
// before a claim is filed (HU-W2.1-01). Faults and latency are injected from
// outside with Toxiproxy; ARTIFICIAL_DELAY_MS is a shortcut for quick local
// runs.
//
// Every holder gets a deterministic set of policies derived from their id, so
// any Cognito subject works without seeding. POLICY_SEED_FILE replaces the
// policies of chosen holders, which is how the tests fix the boundary values.
//
// The evaluation of a claim (HU-W2.2-03) needs more than the list: the core
// also answers one policy of a holder with the insured value and the
// deductible of each coverage, and the holder's profile with the contact the
// insured is notified at and the bank account an indemnity is paid to.
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
	PolicyID         string     `json:"policy_id"`
	Line             string     `json:"line"`
	Product          string     `json:"product"`
	InsuredObject    string     `json:"insured_object"`
	Status           string     `json:"status"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidTo          time.Time  `json:"valid_to"`
	CoveredIncidents []string   `json:"covered_incidents"`
	Coverages        []coverage `json:"coverages"`
}

// coverage is what the policy pays for one covered incident (HU-W2.2-03): an
// approved indemnity cannot exceed InsuredValueMinor, and DeductibleBasisPoints
// of it are discounted before paying (500 is 5 %). Amounts are in minor units
// of Currency, like every amount the gateway handles.
type coverage struct {
	Incident              string `json:"incident"`
	InsuredValueMinor     int64  `json:"insured_value_minor"`
	Currency              string `json:"currency"`
	DeductibleBasisPoints int    `json:"deductible_basis_points"`
}

// holder is the profile of a policyholder: who the insured is, where they are
// notified and the bank account an indemnity is paid to (screen O6).
type holder struct {
	HolderID      string        `json:"holder_id"`
	FullName      string        `json:"full_name"`
	DocumentType  string        `json:"document_type"`
	DocumentID    string        `json:"document_id"`
	Email         string        `json:"email"`
	Phone         string        `json:"phone"`
	PayoutAccount payoutAccount `json:"payout_account"`
}

// payoutAccount is the bank account registered to receive indemnities.
type payoutAccount struct {
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
	AccountType   string `json:"account_type"`
	AccountNumber string `json:"account_number"`
}

type policiesResponse struct {
	Policies []policy `json:"policies"`
}

// seedFile is the shape of POLICY_SEED_FILE: the policies of each holder it
// names, and optionally their profiles. An empty list is a holder without
// policies; a holder without a seeded profile gets the derived one.
type seedFile struct {
	Holders  map[string][]policy `json:"holders"`
	Profiles map[string]holder   `json:"profiles"`
}

// seedData is POLICY_SEED_FILE once loaded.
type seedData struct {
	policies map[string][]policy
	profiles map[string]holder
}

func main() {
	delay := time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond
	seed, err := loadSeed(os.Getenv("POLICY_SEED_FILE"))
	if err != nil {
		log.Fatalf("could not load POLICY_SEED_FILE: %v", err)
	}

	addr := ":" + orDefault(os.Getenv("PORT"), "9060")
	log.Printf("policy-core stub listening on %s (delay=%s, seeded holders=%d)", addr, delay, len(seed.policies))
	srv := &http.Server{Addr: addr, Handler: newHandler(delay, seed, time.Now), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func newHandler(delay time.Duration, seed seedData, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// holderPolicies answers the policies of the holder in the path, or false
	// after writing the 400 of an invalid id.
	holderPolicies := func(w http.ResponseWriter, r *http.Request) (string, []policy, bool) {
		if delay > 0 {
			time.Sleep(delay)
		}
		id := strings.TrimSpace(r.PathValue("holderID"))
		if id == "" || len(id) > maxHolderIDLength {
			http.Error(w, "holder id is required (up to 128 characters)", http.StatusBadRequest)
			return "", nil, false
		}
		policies, seeded := seed.policies[id]
		if !seeded {
			policies = buildPolicies(id, now())
		}
		if policies == nil {
			policies = []policy{}
		}
		return id, policies, true
	}

	mux.HandleFunc("GET /holders/{holderID}/policies", func(w http.ResponseWriter, r *http.Request) {
		if _, policies, ok := holderPolicies(w, r); ok {
			writeJSON(w, http.StatusOK, policiesResponse{Policies: policies})
		}
	})
	// One policy of the holder. The holder is in the path because the claims
	// service knows whose policy it asks for, and because a policy that is not
	// theirs must look exactly like one that does not exist.
	mux.HandleFunc("GET /holders/{holderID}/policies/{policyID}", func(w http.ResponseWriter, r *http.Request) {
		_, policies, ok := holderPolicies(w, r)
		if !ok {
			return
		}
		want := strings.TrimSpace(r.PathValue("policyID"))
		for _, p := range policies {
			if p.PolicyID == want {
				writeJSON(w, http.StatusOK, p)
				return
			}
		}
		http.Error(w, "the holder has no such policy", http.StatusNotFound)
	})
	mux.HandleFunc("GET /holders/{holderID}", func(w http.ResponseWriter, r *http.Request) {
		id, _, ok := holderPolicies(w, r)
		if !ok {
			return
		}
		profile, seeded := seed.profiles[id]
		if !seeded {
			profile = buildHolder(id)
		}
		writeJSON(w, http.StatusOK, profile)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("could not write the response: %v", err)
	}
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

	homeCovers := []string{"water_damage"}
	travelCovers := []string{"flight_delay"}

	return []policy{
		{
			PolicyID: policyID("AU", from, sum), Line: lineAuto, Product: "Autos", InsuredObject: plate(sum),
			Status: statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: autoCovers,
			Coverages: coverages(lineAuto, autoCovers),
		},
		{
			PolicyID: policyID("HG", from, sum/7), Line: lineHome, Product: "Hogar",
			InsuredObject: fmt.Sprintf("Calle %d # %d-%d, Bogotá", 10+sum%150, 1+sum%90, 1+sum%60),
			Status:        statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: homeCovers,
			Coverages: coverages(lineHome, homeCovers),
		},
		{
			PolicyID: policyID("VJ", from, sum/11), Line: lineTravel, Product: "Viaje", InsuredObject: "Viajes nacionales",
			Status: statusActive, ValidFrom: from, ValidTo: to, CoveredIncidents: travelCovers,
			Coverages: coverages(lineTravel, travelCovers),
		},
		{
			PolicyID: policyID("AU", expiredFrom, sum/13), Line: lineAuto, Product: "Autos", InsuredObject: plate(sum / 3),
			Status: statusExpired, ValidFrom: expiredFrom, ValidTo: expiredTo, CoveredIncidents: autoCovers,
			Coverages: coverages(lineAuto, autoCovers),
		},
	}
}

// coverageTerms are the insured value (in COP) and the deductible of every
// coverage of a line. The home figures are the ones of screen O6: 320 million
// insured and a 5 % deductible.
var coverageTerms = map[string]struct {
	insuredValueCOP int64
	deductibleBPS   int
}{
	lineAuto:   {insuredValueCOP: 85_000_000, deductibleBPS: 1000},
	lineHome:   {insuredValueCOP: 320_000_000, deductibleBPS: 500},
	lineTravel: {insuredValueCOP: 5_000_000, deductibleBPS: 0},
}

// coverages gives one coverage per covered incident, with the terms of the
// line.
func coverages(line string, incidents []string) []coverage {
	terms := coverageTerms[line]
	out := make([]coverage, 0, len(incidents))
	for _, incident := range incidents {
		out = append(out, coverage{
			Incident: incident, InsuredValueMinor: terms.insuredValueCOP * 100, Currency: "COP",
			DeductibleBasisPoints: terms.deductibleBPS,
		})
	}
	return out
}

var (
	firstNames = []string{"Carolina", "Andrés", "Luisa", "Mateo", "Valentina", "Santiago", "Daniela", "Julián"}
	lastNames  = []string{"Pérez Ruiz", "Gómez Lara", "Rojas Mejía", "Castro Díaz", "Moreno Ortiz", "Vargas Silva"}
	banks      = []struct{ code, name string }{
		{"1007", "Banco Aliado"}, {"1051", "Banco Central"}, {"1023", "Banco del Valle"},
	}
)

// buildHolder derives the profile of a holder from their id, so any Cognito
// subject has a name, a contact and a payout account without seeding. The
// account number never ends in 89: that ending is what the payment gateway
// stub rejects as an invalid destination (screen O6b), and a derived profile
// must pay.
func buildHolder(id string) holder {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	sum := h.Sum32()

	n := int(sum) // a uint32 always fits in an int
	first, last := firstNames[n%len(firstNames)], lastNames[n/7%len(lastNames)]
	bank := banks[n/11%len(banks)]
	account := fmt.Sprintf("%04d-%04d-%02d", sum%10_000, sum/10_000%10_000, sum/13%88)
	return holder{
		HolderID:     id,
		FullName:     first + " " + last,
		DocumentType: "CC",
		DocumentID:   fmt.Sprintf("%d", 1_000_000_000+sum%99_999_999),
		Email:        fmt.Sprintf("%s.%d@correo.example", strings.ToLower(asciiFold(first)), sum%10_000),
		Phone:        fmt.Sprintf("+57 3%02d %03d %04d", sum%50, sum/50%1000, sum/7%10_000),
		PayoutAccount: payoutAccount{
			BankCode: bank.code, BankName: bank.name, AccountType: "savings", AccountNumber: account,
		},
	}
}

// asciiFold drops the accents of a name, so it can be part of an email.
func asciiFold(s string) string {
	return strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ñ", "n",
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ñ", "N").Replace(s)
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

func loadSeed(path string) (seedData, error) {
	data := seedData{policies: map[string][]policy{}, profiles: map[string]holder{}}
	if path == "" {
		return data, nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return seedData{}, err
	}
	var seed seedFile
	if err := json.Unmarshal(raw, &seed); err != nil {
		return seedData{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if seed.Holders != nil {
		data.policies = seed.Holders
	}
	for id, p := range seed.Profiles {
		p.HolderID = id
		data.profiles[id] = p
	}
	return data, nil
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
