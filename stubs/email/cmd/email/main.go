// Command email is the stub of the e-mail delivery provider. It accepts the
// messages, keeps them in memory and lists them, so a test or a demo can
// verify what the insured would have received. A message with an
// Idempotency-Key already accepted is answered with 200 and not stored again.
// Every new message is also written to the log on one line, with its body, so
// it can be read where GET /messages is out of reach (CloudWatch Logs in AWS,
// HA-D02); a duplicate is logged without its body.
// Faults and latency are injected from outside with Toxiproxy; FAIL_FIRST and
// ARTIFICIAL_DELAY_MS are shortcuts for quick local runs.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBodyBytes = 1 << 16

type message struct {
	ID             string    `json:"id"`
	To             string    `json:"to"`
	Subject        string    `json:"subject"`
	Body           string    `json:"body"`
	IdempotencyKey string    `json:"idempotency_key"`
	ReceivedAt     time.Time `json:"received_at"`
}

type server struct {
	delay     time.Duration
	failFirst int64
	calls     atomic.Int64

	mu    sync.Mutex
	byKey map[string]message
	order []string
}

func main() {
	delay := time.Duration(envInt("ARTIFICIAL_DELAY_MS")) * time.Millisecond
	failFirst := int64(envInt("FAIL_FIRST"))

	addr := ":" + orDefault(os.Getenv("PORT"), "9030")
	log.Printf("email stub listening on %s (delay=%s, fail first=%d)", addr, delay, failFirst)
	srv := &http.Server{Addr: addr, Handler: newHandler(delay, failFirst), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func newHandler(delay time.Duration, failFirst int64) http.Handler {
	s := &server{delay: delay, failFirst: failFirst, byKey: map[string]message{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /messages", s.send)
	mux.HandleFunc("GET /messages", s.list)
	return mux
}

func (s *server) send(w http.ResponseWriter, r *http.Request) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.calls.Add(1) <= s.failFirst {
		http.Error(w, "injected failure", http.StatusServiceUnavailable)
		return
	}
	var in message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		http.Error(w, "the body is not valid JSON", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if in.To == "" || in.Subject == "" || in.Body == "" || key == "" {
		http.Error(w, "to, subject, body and the Idempotency-Key header are required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	existing, duplicate := s.byKey[key]
	if !duplicate {
		existing = message{
			ID: "msg-" + strconv.Itoa(len(s.order)+1), To: in.To, Subject: in.Subject, Body: in.Body,
			IdempotencyKey: key, ReceivedAt: time.Now().UTC(),
		}
		s.byKey[key] = existing
		s.order = append(s.order, key)
	}
	s.mu.Unlock()

	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
		log.Printf("duplicate message id=%s to=%s idempotency_key=%s: already accepted, not stored again",
			existing.ID, existing.To, existing.IdempotencyKey)
	} else {
		// %q keeps the body (and its links) on a single log line.
		log.Printf("message accepted id=%s to=%s subject=%q idempotency_key=%s body=%q",
			existing.ID, existing.To, existing.Subject, existing.IdempotencyKey, existing.Body)
	}
	writeJSON(w, status, existing)
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	s.mu.Lock()
	out := make([]message, 0, len(s.order))
	for _, key := range s.order {
		if m := s.byKey[key]; to == "" || m.To == to {
			out = append(out, m)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
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
