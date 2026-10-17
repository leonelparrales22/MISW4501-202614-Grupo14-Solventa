// Command pasarela-pagos is the stub of the payment gateway (HA-D02). It
// charges premiums the way a real provider that honors idempotency keys does,
// and it counts what it did so an experiment can prove that no charge was lost
// and none was duplicated.
//
// Two facts make the experiment meaningful (EC-D02):
//
//   - A charge is processed and recorded BEFORE the answer is written. When
//     Toxiproxy cuts the answer (the "ambiguous" fault), the charge already
//     happened even though the caller never learned it.
//   - The stub tells apart the requests it received from the real charges it
//     made. A retry with a key already charged returns the same answer without
//     charging again (SUPPORTS_IDEMPOTENCY=true, the default). Counting rows in
//     the caller's database can never reveal a duplicate; real_charges > 1 for
//     one key can. SUPPORTS_IDEMPOTENCY=false simulates a provider that does
//     not honor the key, for the counterfactual run.
//
// A charge whose reference contains "declined" is declined (402, decline code
// 51), so the explicit rejection can be shown with a regular demo offer.
// Faults and latency are injected from outside with Toxiproxy;
// ARTIFICIAL_DELAY_MS delays every answer after the charge is recorded.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxBodyBytes   = 1 << 14
	declineMarker  = "declined"
	declineCode    = "51"
	statusSuccess  = "succeeded"
	defaultPort    = "9040"
	maxKeyLength   = 255
	maxFieldLength = 256
)

type chargeRequest struct {
	Reference   string `json:"reference"`
	CustomerID  string `json:"customer_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type charge struct {
	ID             string    `json:"charge_id"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	Reference      string    `json:"reference"`
	CustomerID     string    `json:"customer_id"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
	CreatedAt      time.Time `json:"created_at"`
}

type errorBody struct {
	Error       string `json:"error"`
	Message     string `json:"message"`
	DeclineCode string `json:"decline_code,omitempty"`
}

// result is the stored answer of a key: what a retry with the same key gets.
type result struct {
	req    chargeRequest
	status int
	body   any
}

type gateway struct {
	delay               time.Duration
	supportsIdempotency bool

	mu          sync.Mutex
	byKey       map[string]result
	requests    map[string]int
	realCharges map[string]int
	total       int
	seq         int
}

func main() {
	delay := time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond
	supports := os.Getenv("SUPPORTS_IDEMPOTENCY") != "false"
	addr := ":" + orDefault(os.Getenv("PORT"), defaultPort)
	log.Printf("payment gateway stub listening on %s (delay=%s, supports idempotency=%t)", addr, delay, supports)
	srv := &http.Server{Addr: addr, Handler: newHandler(delay, supports), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func newHandler(delay time.Duration, supportsIdempotency bool) http.Handler {
	g := &gateway{delay: delay, supportsIdempotency: supportsIdempotency}
	g.reset()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /charges", g.charge)
	mux.HandleFunc("GET /debug/charges", g.debug)
	mux.HandleFunc("POST /debug/reset", func(w http.ResponseWriter, _ *http.Request) {
		g.reset()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (g *gateway) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byKey, g.requests, g.realCharges = map[string]result{}, map[string]int{}, map[string]int{}
	g.total, g.seq = 0, 0
}

func (g *gateway) charge(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxKeyLength {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request", Message: "the Idempotency-Key header (up to 255 characters) is required"})
		return
	}
	var in chargeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request", Message: "the body is not valid JSON"})
		return
	}
	if !valid(in) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request",
			Message: "reference, customer_id, a positive amount_minor and currency are required"})
		return
	}

	// The charge is processed and recorded before the answer is written.
	status, body, newCharge := g.process(key, in)
	log.Printf("charge request key=%s reference=%s status=%d new_charge=%t", key, in.Reference, status, newCharge)
	if g.delay > 0 {
		time.Sleep(g.delay)
	}
	writeJSON(w, status, body)
}

// process decides the answer of one request and records it.
func (g *gateway) process(key string, in chargeRequest) (status int, body any, newCharge bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests[key]++
	g.total++

	if prev, seen := g.byKey[key]; seen && g.supportsIdempotency {
		if prev.req != in {
			return http.StatusUnprocessableEntity, errorBody{Error: "idempotency_key_reused",
				Message: "this Idempotency-Key was used with a different charge"}, false
		}
		replay := prev.status
		if replay == http.StatusCreated {
			replay = http.StatusOK
		}
		return replay, prev.body, false
	}

	var res result
	if strings.Contains(in.Reference, declineMarker) {
		res = result{req: in, status: http.StatusPaymentRequired, body: errorBody{
			Error: "payment_declined", Message: "the issuer declined the charge", DeclineCode: declineCode}}
	} else {
		g.seq++
		g.realCharges[key]++
		res = result{req: in, status: http.StatusCreated, body: charge{
			ID: "ch-" + strconv.Itoa(g.seq), Status: statusSuccess, IdempotencyKey: key, Reference: in.Reference,
			CustomerID: in.CustomerID, AmountMinor: in.AmountMinor, Currency: in.Currency, CreatedAt: time.Now().UTC(),
		}}
		newCharge = true
	}
	g.byKey[key] = res
	return res.status, res.body, newCharge
}

func valid(in chargeRequest) bool {
	return in.Reference != "" && in.CustomerID != "" && in.AmountMinor > 0 && in.Currency != "" &&
		len(in.Reference) <= maxFieldLength && len(in.CustomerID) <= maxFieldLength && len(in.Currency) <= 8
}

// debug lists, per key, the requests received and the real charges made.
func (g *gateway) debug(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	requests := make(map[string]int, len(g.requests))
	for k, v := range g.requests {
		requests[k] = v
	}
	charged := make(map[string]int, len(g.realCharges))
	totalReal := 0
	for k, v := range g.realCharges {
		charged[k] = v
		totalReal += v
	}
	total := g.total
	g.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"requests":             requests,
		"real_charges":         charged,
		"total_requests":       total,
		"total_real_charges":   totalReal,
		"supports_idempotency": g.supportsIdempotency,
	})
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

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
