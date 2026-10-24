// Command openfinance is the stub of the Open Finance provider. Faults and
// latency are injected from outside with Toxiproxy; ARTIFICIAL_DELAY_MS is a
// shortcut for quick local runs.
//
// Besides the profiles, it receives the notices of revoked consents
// (HU-M1.3-02): POST /consent-revocations with a mandatory Idempotency-Key is
// recorded once per key (201 the first time, 200 with the same result
// after). REVOCATION_FAIL_FIRST answers 503 to the first N notices and
// REVOCATION_UNRESPONSIVE_ENTITIES always answers 503 for the listed entity
// codes, to test the retries and the partial revocation. Every notice and
// every profile request is written to the log on one line, so the evidence
// can be read where GET /debug/consent-revocations is out of reach
// (CloudWatch Logs in AWS).
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBodyBytes = 1 << 14

type profile struct {
	CustomerID string    `json:"customer_id"`
	Score      float64   `json:"score"`
	Category   string    `json:"category"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// revocationNotice is what audit sends when a consent is revoked.
type revocationNotice struct {
	ConsentID   string    `json:"consent_id"`
	EntityCode  string    `json:"entity_code"`
	CustomerID  string    `json:"customer_id"`
	EffectiveAt time.Time `json:"effective_at"`
}

// revocation is the stub's record of a notice it accepted.
type revocation struct {
	Reference      string    `json:"revocation_reference"`
	Status         string    `json:"status"`
	ConfirmedAt    time.Time `json:"confirmed_at"`
	ConsentID      string    `json:"consent_id"`
	EntityCode     string    `json:"entity_code"`
	IdempotencyKey string    `json:"idempotency_key"`
}

// options configure the stub.
type options struct {
	delay time.Duration
	// failFirst is the number of first notices answered with 503.
	failFirst int64
	// unresponsive are the entity codes whose notices always get 503.
	unresponsive map[string]bool
}

type server struct {
	opts options

	notices atomic.Int64

	mu sync.Mutex
	// requests counts every notice received (with a key), new or repeated.
	requests int
	byKey    map[string]revocation
	order    []string
}

func main() {
	opts := options{
		delay:        time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond,
		failFirst:    int64(envInt("REVOCATION_FAIL_FIRST")),
		unresponsive: envSet("REVOCATION_UNRESPONSIVE_ENTITIES"),
	}
	addr := ":" + orDefault(os.Getenv("PORT"), "9000")
	log.Printf("openfinance stub listening on %s (delay=%s, revocation fail first=%d, unresponsive entities=%d)",
		addr, opts.delay, opts.failFirst, len(opts.unresponsive))
	srv := &http.Server{Addr: addr, Handler: newServer(opts).routes(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// newHandler serves the stub with only an artificial delay.
func newHandler(delay time.Duration) http.Handler {
	return newServer(options{delay: delay}).routes()
}

func newServer(opts options) *server {
	if opts.unresponsive == nil {
		opts.unresponsive = map[string]bool{}
	}
	return &server{opts: opts, byKey: map[string]revocation{}}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /profiles/{customerID}", s.profile)
	mux.HandleFunc("POST /consent-revocations", s.revoke)
	mux.HandleFunc("GET /debug/consent-revocations", s.debug)
	return mux
}

func (s *server) profile(w http.ResponseWriter, r *http.Request) {
	if s.opts.delay > 0 {
		time.Sleep(s.opts.delay)
	}
	id := strings.TrimSpace(r.PathValue("customerID"))
	if id == "" {
		http.Error(w, "customer id is required", http.StatusBadRequest)
		return
	}
	// Evidence that a revoked consent stops the queries (HU-M1.3-02
	// criterion 3): every profile request leaves one line.
	log.Printf("profile request customer=%s", id)
	writeJSON(w, http.StatusOK, buildProfile(id, time.Now()))
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	if s.opts.delay > 0 {
		time.Sleep(s.opts.delay)
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		http.Error(w, "the Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var in revocationNotice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		http.Error(w, "the body is not valid JSON", http.StatusBadRequest)
		return
	}
	if in.ConsentID == "" || in.EntityCode == "" {
		http.Error(w, "consent_id and entity_code are required", http.StatusBadRequest)
		return
	}
	if s.notices.Add(1) <= s.opts.failFirst {
		log.Printf("consent revocation key=%s entity=%s consent=%s new=false injected=fail_first", key, in.EntityCode, in.ConsentID)
		http.Error(w, "injected failure", http.StatusServiceUnavailable)
		return
	}
	if s.opts.unresponsive[in.EntityCode] {
		log.Printf("consent revocation key=%s entity=%s consent=%s new=false injected=unresponsive", key, in.EntityCode, in.ConsentID)
		http.Error(w, "the entity does not answer", http.StatusServiceUnavailable)
		return
	}

	s.mu.Lock()
	s.requests++
	rev, repeated := s.byKey[key]
	if !repeated {
		rev = revocation{
			Reference: fmt.Sprintf("of-rev-%d", len(s.order)+1), Status: "confirmed", ConfirmedAt: time.Now().UTC(),
			ConsentID: in.ConsentID, EntityCode: in.EntityCode, IdempotencyKey: key,
		}
		s.byKey[key] = rev
		s.order = append(s.order, key)
	}
	s.mu.Unlock()

	log.Printf("consent revocation key=%s entity=%s consent=%s new=%t", key, in.EntityCode, in.ConsentID, !repeated)
	status := http.StatusCreated
	if repeated {
		status = http.StatusOK
	}
	writeJSON(w, status, rev)
}

// debug separates the notices received from the unique revocations by key:
// with an honored key, a retried notice never revokes twice.
func (s *server) debug(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]revocation, 0, len(s.order))
	for _, key := range s.order {
		out = append(out, s.byKey[key])
	}
	requests := s.requests
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"requests_received":     requests,
		"failures_injected":     max(0, s.notices.Load()-int64(requests)),
		"unique_revocations":    len(out),
		"revocations":           out,
		"unresponsive_entities": sortedKeys(s.opts.unresponsive),
	})
}

// buildProfile derives a deterministic profile from the customer id.
func buildProfile(id string, now time.Time) profile {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	score := float64(h.Sum32()%1000) / 1000.0
	category := "standard"
	switch {
	case score > 0.8:
		category = "high_risk"
	case score < 0.2:
		category = "preferred"
	}
	return profile{CustomerID: id, Score: score, Category: category, UpdatedAt: now.UTC()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("could not write the response: %v", err)
	}
}

func envInt(name string) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// envSet reads a comma-separated list into a set; empty items are ignored.
func envSet(name string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(os.Getenv(name), ",") {
		if item = strings.TrimSpace(item); item != "" {
			out[item] = true
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
