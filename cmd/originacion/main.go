package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	listen, redis, upstream             string
	timeout, ttl, recovery              time.Duration
	threshold, halfOpenSuccessThreshold int
}
type breakerState string

const (
	closed   breakerState = "closed"
	open     breakerState = "open"
	halfOpen breakerState = "half_open"
)

var errCircuitOpen = errors.New("circuit breaker is open")

type circuitBreaker struct {
	mu                                sync.Mutex
	state                             breakerState
	failures, threshold               int
	halfOpenSuccesses, halfOpenTarget int
	halfOpenInFlight                  bool
	recovery                          time.Duration
	openedAt                          time.Time
}

func (b *circuitBreaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == closed {
		return nil
	}
	if b.state == open {
		if time.Since(b.openedAt) < b.recovery {
			return errCircuitOpen
		}
		b.state, b.halfOpenSuccesses, b.halfOpenInFlight = halfOpen, 0, true
		log.Printf("event=circuit_breaker_transition from=open to=half_open")
		return nil
	}
	if b.halfOpenInFlight {
		return errCircuitOpen
	}
	b.halfOpenInFlight = true
	return nil
}
func (b *circuitBreaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == halfOpen {
		b.halfOpenInFlight = false
		b.halfOpenSuccesses++
		if b.halfOpenSuccesses < b.halfOpenTarget {
			log.Printf("event=circuit_breaker_half_open_success count=%d target=%d", b.halfOpenSuccesses, b.halfOpenTarget)
			return
		}
		log.Printf("event=circuit_breaker_transition from=half_open to=closed successes=%d", b.halfOpenSuccesses)
	}
	b.state, b.failures, b.halfOpenSuccesses, b.halfOpenInFlight = closed, 0, 0, false
}
func (b *circuitBreaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == halfOpen {
		b.halfOpenInFlight, b.halfOpenSuccesses = false, 0
		log.Printf("event=circuit_breaker_transition from=half_open to=open")
		b.state, b.openedAt = open, time.Now()
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		if b.state != open {
			log.Printf("event=circuit_breaker_transition from=%s to=open failures=%d", b.state, b.failures)
		}
		b.state, b.openedAt = open, time.Now()
	}
}
func (b *circuitBreaker) current() breakerState { b.mu.Lock(); defer b.mu.Unlock(); return b.state }

// redisClient implements the small RESP subset needed by this dependency-free POC.
type redisClient struct{ addr string }

func (r redisClient) command(parts ...string) ([]string, error) {
	conn, err := net.DialTimeout("tcp", r.addr, 200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	var request strings.Builder
	fmt.Fprintf(&request, "*%d\r\n", len(parts))
	for _, p := range parts {
		fmt.Fprintf(&request, "$%d\r\n%s\r\n", len(p), p)
	}
	if _, err = conn.Write([]byte(request.String())); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "-") {
		return nil, errors.New(line[1:])
	}
	if strings.HasPrefix(line, "+") {
		return []string{line[1:]}, nil
	}
	if line == "$-1" {
		return nil, nil
	}
	if strings.HasPrefix(line, "$") {
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		value := make([]byte, n+2)
		if _, err = reader.Read(value); err != nil {
			return nil, err
		}
		return []string{string(value[:n])}, nil
	}
	return nil, fmt.Errorf("unexpected Redis response %q", line)
}
func (r redisClient) get(key string) (string, bool, error) {
	values, err := r.command("GET", key)
	if err != nil || values == nil {
		return "", false, err
	}
	return values[0], true, nil
}
func (r redisClient) set(key, value string, ttl time.Duration) error {
	_, err := r.command("SET", key, value, "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

type offerRequest struct {
	CustomerID string `json:"customer_id"`
}
type offerResponse struct {
	CustomerID   string       `json:"customer_id"`
	RiskProfile  string       `json:"risk_profile"`
	Source       string       `json:"source"`
	Provisional  bool         `json:"provisional"`
	CircuitState breakerState `json:"circuit_breaker_state"`
}
type application struct {
	cfg     config
	cache   redisClient
	breaker *circuitBreaker
	client  *http.Client
}

func (a application) offer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	var body offerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CustomerID == "" {
		http.Error(w, "customer_id is required", http.StatusBadRequest)
		return
	}
	profile, source, provisional := a.resolve(r.Context(), body.CustomerID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(offerResponse{body.CustomerID, profile, source, provisional, a.breaker.current()})
}
func (a application) resolve(parent context.Context, customerID string) (string, string, bool) {
	if err := a.breaker.allow(); err == nil {
		ctx, cancel := context.WithTimeout(parent, a.cfg.timeout)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.upstream+"/profiles/"+customerID, nil)
		response, err := a.client.Do(req)
		if err == nil && response.StatusCode == http.StatusOK {
			defer response.Body.Close()
			var data struct {
				RiskProfile string `json:"risk_profile"`
			}
			if json.NewDecoder(response.Body).Decode(&data) == nil && data.RiskProfile != "" {
				a.breaker.success()
				if err := a.cache.set("risk:"+customerID, data.RiskProfile, a.cfg.ttl); err != nil {
					log.Printf("event=cache_write_failed error=%q", err)
				}
				return data.RiskProfile, "open_finance", false
			}
		}
		if response != nil {
			response.Body.Close()
		}
		a.breaker.failure()
		log.Printf("event=open_finance_failure customer_id=%s error=%q", customerID, err)
	}
	if profile, found, err := a.cache.get("risk:" + customerID); err == nil && found {
		return profile, "cache", true
	} else if err != nil {
		log.Printf("event=cache_read_failed error=%q", err)
	}
	return "actuarial_base", "actuarial_base", true
}
func (a application) health(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
func value(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func duration(k string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(value(k, ""))
	if err == nil && v > 0 {
		return v
	}
	return fallback
}
func integer(k string, fallback int) int {
	v, err := strconv.Atoi(value(k, ""))
	if err == nil && v > 0 {
		return v
	}
	return fallback
}
func main() {
	cfg := config{listen: value("LISTEN_ADDR", ":8080"), redis: value("REDIS_ADDR", "redis:6379"), upstream: strings.TrimSuffix(value("OPEN_FINANCE_URL", "http://toxiproxy:8666"), "/"), timeout: duration("EXTERNAL_TIMEOUT", 700*time.Millisecond), ttl: duration("RISK_PROFILE_CACHE_TTL", 24*time.Hour), threshold: integer("CB_FAILURE_THRESHOLD", 3), halfOpenSuccessThreshold: integer("CB_HALF_OPEN_SUCCESS_THRESHOLD", 3), recovery: duration("CB_RECOVERY_WINDOW", 5*time.Second)}
	app := application{cfg, redisClient{cfg.redis}, &circuitBreaker{state: closed, threshold: cfg.threshold, halfOpenTarget: cfg.halfOpenSuccessThreshold, recovery: cfg.recovery}, &http.Client{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /offers", app.offer)
	mux.HandleFunc("GET /health", app.health)
	log.Printf("event=service_started timeout=%s ttl=%s threshold=%d half_open_success_target=%d", cfg.timeout, cfg.ttl, cfg.threshold, cfg.halfOpenSuccessThreshold)
	log.Fatal(http.ListenAndServe(cfg.listen, mux))
}
