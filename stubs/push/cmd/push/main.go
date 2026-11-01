// Command push is the stub of the push notification provider (Firebase Cloud
// Messaging in the architecture, HU-M3.1-01). The project integrates no real
// provider: notifications sends its messages here and a test or a demo checks
// what the device would have received. A message with an Idempotency-Key
// already accepted is answered with 200 and not stored again.
//
// Every new message is written to the log on one line, so it can be read
// where GET /messages is out of reach. A token that starts with "invalid-" is
// answered with 404 unregistered, like a provider that no longer knows the
// device; FAIL_FIRST (the first N sends answer 503) and ARTIFICIAL_DELAY_MS
// are shortcuts for the retry scenarios. Faults can also be injected from
// outside with Toxiproxy.
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

// unregisteredPrefix marks the tokens the stub treats as no longer valid.
const unregisteredPrefix = "invalid-"

type message struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Group is the thread the device groups the message in; each message
	// keeps its own entry inside it (HU-M3.1-01, criterion 6).
	Group          string            `json:"group"`
	Data           map[string]string `json:"data"`
	IdempotencyKey string            `json:"idempotency_key"`
	ReceivedAt     time.Time         `json:"received_at"`
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

	addr := ":" + orDefault(os.Getenv("PORT"), "9070")
	log.Printf("push stub listening on %s (delay=%s, fail first=%d)", addr, delay, failFirst)
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
	if in.Token == "" || in.Title == "" || in.Body == "" || key == "" {
		http.Error(w, "token, title, body and the Idempotency-Key header are required", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(in.Token, unregisteredPrefix) {
		log.Printf("message rejected token=%s idempotency_key=%s: unregistered device", in.Token, key)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unregistered"})
		return
	}

	s.mu.Lock()
	existing, duplicate := s.byKey[key]
	if !duplicate {
		existing = message{
			ID: "push-" + strconv.Itoa(len(s.order)+1), Token: in.Token, Title: in.Title, Body: in.Body, Group: in.Group,
			Data: in.Data, IdempotencyKey: key, ReceivedAt: time.Now().UTC(),
		}
		s.byKey[key] = existing
		s.order = append(s.order, key)
	}
	s.mu.Unlock()

	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
		log.Printf("duplicate message id=%s token=%s idempotency_key=%s: already accepted, not stored again",
			existing.ID, existing.Token, existing.IdempotencyKey)
	} else {
		// %q keeps the title and body on a single log line.
		log.Printf("message accepted id=%s token=%s group=%s title=%q body=%q idempotency_key=%s",
			existing.ID, existing.Token, existing.Group, existing.Title, existing.Body, existing.IdempotencyKey)
	}
	writeJSON(w, status, existing)
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	s.mu.Lock()
	out := make([]message, 0, len(s.order))
	for _, key := range s.order {
		if m := s.byKey[key]; token == "" || m.Token == token {
			out = append(out, m)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"messages": out, "requests": s.calls.Load()})
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
