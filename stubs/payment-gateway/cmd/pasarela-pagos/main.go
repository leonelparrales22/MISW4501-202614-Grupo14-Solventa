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
//
// POST /tokens tokenises a card the way a real gateway does (HU-W1.3-02): the
// browser sends the card straight here and gets back a token, so the number
// never reaches Solventa and the platform stays out of PCI-DSS scope. The
// stub keeps the brand and the last four digits of the token, never the
// number. A charge that carries a payment_token is decided by the card behind
// it: the conventional test card 4111 1111 1111 1111 is approved and anything
// ending in 0002 is declined for insufficient funds, which is what makes
// screen E4c reachable in a demo. A charge without a token behaves as before.
//
// POST /payouts pays an indemnity to a bank account (HU-W2.2-03) with the same
// idempotency rules: a key pays once, and a retry gets the same payout. Two
// differences follow from what a payout is. An account number ending in 89 is
// rejected as an invalid destination (422), which is what makes screen O6b
// reachable in a demo. And a rejected payout moved no money, so it does not
// bind its key: the analyst corrects the account and retries with the same
// key, which is what keeps one approval to exactly one disbursement.
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

	// Conventional test cards. Approving one number and declining another is
	// what lets a demo walk both branches of screen E4 without touching code.
	approvedTestCard   = "4111111111111111"
	declinedCardSuffix = "0002"
	insufficientFunds  = "insufficient_funds"
	tokenPrefix        = "tok_"
	maxTokenLength     = 128

	// The account ending that the stub rejects as an invalid destination, the
	// one screen O6b shows (0123-4567-89).
	invalidAccountSuffix = "89"
	statusPaid           = "paid"
)

// payoutDestination is the bank account an indemnity is paid to.
type payoutDestination struct {
	BankCode      string `json:"bank_code"`
	AccountType   string `json:"account_type"`
	AccountNumber string `json:"account_number"`
}

type payoutRequest struct {
	Reference     string            `json:"reference"`
	BeneficiaryID string            `json:"beneficiary_id"`
	AmountMinor   int64             `json:"amount_minor"`
	Currency      string            `json:"currency"`
	Destination   payoutDestination `json:"destination"`
}

type payout struct {
	ID             string            `json:"payout_id"`
	Status         string            `json:"status"`
	IdempotencyKey string            `json:"idempotency_key"`
	Reference      string            `json:"reference"`
	BeneficiaryID  string            `json:"beneficiary_id"`
	AmountMinor    int64             `json:"amount_minor"`
	Currency       string            `json:"currency"`
	Destination    payoutDestination `json:"destination"`
	CreatedAt      time.Time         `json:"created_at"`
}

// payoutResult is the stored payout of a key: what a retry with it gets.
type payoutResult struct {
	req  payoutRequest
	body payout
}

type chargeRequest struct {
	Reference   string `json:"reference"`
	CustomerID  string `json:"customer_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	// PaymentToken is the token of POST /tokens. Empty keeps the behaviour
	// the charge had before HU-W1.3-02, so nothing that already calls this
	// stub breaks.
	PaymentToken string `json:"payment_token,omitempty"`
}

// tokenRequest is a card on its way to being tokenised. It only lives inside
// this handler: nothing of it is stored but the brand and the last four.
type tokenRequest struct {
	Number string `json:"number"`
	Holder string `json:"holder"`
}

type tokenResponse struct {
	Token string `json:"token"`
	Brand string `json:"brand"`
	Last4 string `json:"last4"`
}

// tokenInfo is what the gateway remembers of a token: enough to show it and
// to decide the charge, never the number.
type tokenInfo struct {
	Brand    string
	Last4    string
	Declines bool
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
	Error   string `json:"error"`
	Message string `json:"message"`
	// ChargeID is the gateway's reference for the declined charge. A real
	// gateway hands one over whatever the outcome, and the customer needs it
	// to talk to the issuer about the decline (screen E4c, HU-W1.3-02).
	ChargeID    string `json:"charge_id,omitempty"`
	DeclineCode string `json:"decline_code,omitempty"`
	// PayoutID is the gateway's reference for a rejected payout.
	PayoutID string `json:"payout_id,omitempty"`
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
	byToken     map[string]tokenInfo
	requests    map[string]int
	realCharges map[string]int
	total       int
	seq         int
	tokenSeq    int

	// Payouts are counted apart from charges, so the figures of the charge
	// experiment (EC-D02) are not mixed with indemnities.
	payoutByKey    map[string]payoutResult
	payoutRequests map[string]int
	realPayouts    map[string]int
	payoutSeq      int
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
	mux.HandleFunc("POST /tokens", g.tokenise)
	mux.HandleFunc("POST /charges", g.charge)
	mux.HandleFunc("POST /payouts", g.payout)
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
	g.byToken = map[string]tokenInfo{}
	g.total, g.seq, g.tokenSeq = 0, 0, 0
	g.payoutByKey, g.payoutRequests, g.realPayouts = map[string]payoutResult{}, map[string]int{}, map[string]int{}
	g.payoutSeq = 0
}

// tokenise turns a card into a token. This is the endpoint the browser calls
// directly, so the number never travels through Solventa: only the token, the
// brand and the last four digits come back, and only those are kept here.
func (g *gateway) tokenise(w http.ResponseWriter, r *http.Request) {
	var in tokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request", Message: "the body is not valid JSON"})
		return
	}
	number := onlyDigits(in.Number)
	if len(number) < 13 || len(number) > 19 || !luhn(number) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_card",
			Message: "the card number is not valid"})
		return
	}

	last4 := number[len(number)-4:]
	info := tokenInfo{
		Brand: brandOf(number),
		Last4: last4,
		// The decision is fixed when the card is tokenised, not when it is
		// charged: the number is gone by then, which is the whole point.
		Declines: number != approvedTestCard && strings.HasSuffix(number, declinedCardSuffix),
	}

	g.mu.Lock()
	g.tokenSeq++
	token := tokenPrefix + strconv.Itoa(g.tokenSeq) + last4
	g.byToken[token] = info
	g.mu.Unlock()

	log.Printf("card tokenised token=%s brand=%s last4=%s declines=%t", token, info.Brand, info.Last4, info.Declines)
	writeJSON(w, http.StatusCreated, tokenResponse{Token: token, Brand: info.Brand, Last4: info.Last4})
}

// luhn is the checksum every card number satisfies. Rejecting a number that
// fails it is what a real gateway does, and it keeps a typo from becoming a
// charge that is declined for the wrong reason.
func luhn(number string) bool {
	sum, double := 0, false
	for i := len(number) - 1; i >= 0; i-- {
		d := int(number[i] - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func brandOf(number string) string {
	switch {
	case strings.HasPrefix(number, "4"):
		return "visa"
	case strings.HasPrefix(number, "34"), strings.HasPrefix(number, "37"):
		return "amex"
	case strings.HasPrefix(number, "5"), strings.HasPrefix(number, "2"):
		return "mastercard"
	default:
		return "unknown"
	}
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
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

	declined, reason := g.decide(in)

	var res result
	if declined {
		g.seq++
		res = result{req: in, status: http.StatusPaymentRequired, body: errorBody{
			Error: "payment_declined", Message: reason, ChargeID: "ch-" + strconv.Itoa(g.seq),
			DeclineCode: declineCode}}
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

// decide tells whether a charge is declined and why. A charge that carries a
// token is decided by the card behind it; one without a token keeps the old
// behaviour, so whatever already calls this stub is unaffected. The caller
// holds the lock.
func (g *gateway) decide(in chargeRequest) (declined bool, reason string) {
	if in.PaymentToken != "" {
		info, known := g.byToken[in.PaymentToken]
		if !known {
			return true, "the payment token is unknown or expired"
		}
		if info.Declines {
			return true, "the issuer declined the charge: " + insufficientFunds
		}
		return false, ""
	}
	if strings.Contains(in.Reference, declineMarker) {
		return true, "the issuer declined the charge"
	}
	return false, ""
}

func valid(in chargeRequest) bool {
	if in.Reference == "" || in.CustomerID == "" || in.AmountMinor <= 0 || in.Currency == "" ||
		len(in.Reference) > maxFieldLength || len(in.CustomerID) > maxFieldLength || len(in.Currency) > 8 {
		return false
	}
	// A caller that sends a card number where the token goes is a bug worth
	// failing loudly: the number must never arrive here through the charge.
	digits := onlyDigits(in.PaymentToken)
	if len(in.PaymentToken) > maxTokenLength || (len(digits) >= 13 && digits == in.PaymentToken) {
		return false
	}
	return true
}

// payout pays an indemnity. Like a charge, it is recorded before the answer
// is written, so a cut answer still leaves the payout made.
func (g *gateway) payout(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxKeyLength {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request", Message: "the Idempotency-Key header (up to 255 characters) is required"})
		return
	}
	var in payoutRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request", Message: "the body is not valid JSON"})
		return
	}
	if !validPayout(in) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid_request",
			Message: "reference, beneficiary_id, a positive amount_minor, currency and a destination with bank_code and a 6 to 20 digit account_number are required"})
		return
	}

	status, body, newPayout := g.processPayout(key, in)
	log.Printf("payout request key=%s reference=%s status=%d new_payout=%t", key, in.Reference, status, newPayout)
	if g.delay > 0 {
		time.Sleep(g.delay)
	}
	writeJSON(w, status, body)
}

func (g *gateway) processPayout(key string, in payoutRequest) (status int, body any, newPayout bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.payoutRequests[key]++

	if prev, seen := g.payoutByKey[key]; seen && g.supportsIdempotency {
		if prev.req != in {
			return http.StatusUnprocessableEntity, errorBody{Error: "idempotency_key_reused",
				Message: "this Idempotency-Key already paid a different payout"}, false
		}
		return http.StatusOK, prev.body, false
	}

	g.payoutSeq++
	id := "po-" + strconv.Itoa(g.payoutSeq)
	if strings.HasSuffix(onlyDigits(in.Destination.AccountNumber), invalidAccountSuffix) {
		// Nothing was paid, so the key stays free for the corrected account.
		return http.StatusUnprocessableEntity, errorBody{Error: "invalid_destination_account",
			Message: "the destination account number is not valid according to the bank", PayoutID: id}, false
	}

	g.realPayouts[key]++
	paid := payout{
		ID: id, Status: statusPaid, IdempotencyKey: key, Reference: in.Reference, BeneficiaryID: in.BeneficiaryID,
		AmountMinor: in.AmountMinor, Currency: in.Currency, Destination: in.Destination, CreatedAt: time.Now().UTC(),
	}
	g.payoutByKey[key] = payoutResult{req: in, body: paid}
	return http.StatusCreated, paid, true
}

func validPayout(in payoutRequest) bool {
	account := onlyDigits(in.Destination.AccountNumber)
	return in.Reference != "" && in.BeneficiaryID != "" && in.AmountMinor > 0 && in.Currency != "" &&
		len(in.Reference) <= maxFieldLength && len(in.BeneficiaryID) <= maxFieldLength && len(in.Currency) <= 8 &&
		in.Destination.BankCode != "" && len(in.Destination.BankCode) <= 16 &&
		len(in.Destination.AccountType) <= 32 && len(account) >= 6 && len(account) <= 20 &&
		len(in.Destination.AccountNumber) <= 32
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
	payoutRequests := make(map[string]int, len(g.payoutRequests))
	for k, v := range g.payoutRequests {
		payoutRequests[k] = v
	}
	paid := make(map[string]int, len(g.realPayouts))
	totalPaid := 0
	for k, v := range g.realPayouts {
		paid[k] = v
		totalPaid += v
	}
	g.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"requests":             requests,
		"real_charges":         charged,
		"total_requests":       total,
		"total_real_charges":   totalReal,
		"payout_requests":      payoutRequests,
		"real_payouts":         paid,
		"total_real_payouts":   totalPaid,
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
