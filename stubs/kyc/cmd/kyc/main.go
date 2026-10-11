// Command kyc is the stub of the KYC provider (identity verification,
// restrictive lists and AML checks). Faults and latency are injected from
// outside with Toxiproxy; ARTIFICIAL_DELAY_MS is a shortcut for quick local
// runs.
package main

import (
	"encoding/json"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// hashSalt makes the KYC signal differ from the other stubs for the same
// customer.
const hashSalt = "kyc:"

type profile struct {
	CustomerID string    `json:"customer_id"`
	Score      float64   `json:"score"`
	Category   string    `json:"category"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func main() {
	delay := time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond

	addr := ":" + orDefault(os.Getenv("PORT"), "9020")
	log.Printf("kyc stub listening on %s (delay=%s)", addr, delay)
	srv := &http.Server{Addr: addr, Handler: newHandler(delay), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func newHandler(delay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /profiles/{customerID}", func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		id := strings.TrimSpace(r.PathValue("customerID"))
		if id == "" {
			http.Error(w, "customer id is required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(buildProfile(id, time.Now())); err != nil {
			log.Printf("could not write the response: %v", err)
		}
	})
	return mux
}

// buildProfile derives a deterministic profile from the customer id.
func buildProfile(id string, now time.Time) profile {
	h := fnv.New32a()
	_, _ = h.Write([]byte(hashSalt + id))
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
