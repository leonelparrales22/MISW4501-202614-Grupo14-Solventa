package decision_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-03: aprobar paga la indemnización, rechazar avisa al asegurado y un
// pago fallido queda como excepción que el analista reintenta.

var inicio = time.Date(2026, 9, 16, 16, 40, 0, 0, time.UTC)

type reloj struct {
	mu  sync.Mutex
	now time.Time
}

func (r *reloj) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *reloj) avanzar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
}

type nucleo struct {
	terms     domain.PolicyTerms
	holder    domain.Holder
	errTerms  error
	errHolder error
}

func (n nucleo) PolicyTerms(context.Context, string, string) (domain.PolicyTerms, error) {
	return n.terms, n.errTerms
}

func (n nucleo) Holder(context.Context, string) (domain.Holder, error) { return n.holder, n.errHolder }

// pasarela responde lo que diga answer y cuenta los pagos que hizo de verdad:
// un pago por llave, como la pasarela real.
type pasarela struct {
	mu      sync.Mutex
	answer  func(d domain.Decision) error
	calls   []domain.Decision
	pagados map[string]bool
}

func (p *pasarela) Payout(_ context.Context, d domain.Decision) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, d)
	if p.answer != nil {
		if err := p.answer(d); err != nil {
			return "", err
		}
	}
	if p.pagados == nil {
		p.pagados = map[string]bool{}
	}
	p.pagados[d.Payout.Key] = true
	return "po-" + d.ClaimID, nil
}

type correo struct {
	err   error
	sent  []string
	calls int
}

func (c *correo) Send(_ context.Context, to, subject, body, key string) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, to+"|"+subject+"|"+body+"|"+key)
	return nil
}

var agua = domain.Coverage{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 32_000_000_000, Currency: "COP",
	DeductibleBasisPoints: 500}

var cuenta = domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
	AccountNumber: "0123-4567-89"}

func nucleoDeCarolina() nucleo {
	return nucleo{
		terms:  domain.PolicyTerms{Policy: domain.Policy{ID: "HG-2025-880231"}, Coverages: []domain.Coverage{agua}},
		holder: domain.Holder{FullName: "Carolina Pérez Ruiz", Email: "carolina@correo.example", PayoutAccount: cuenta},
	}
}

type escenario struct {
	cases   *memory.CaseStore
	store   *memory.DecisionStore
	reloj   *reloj
	gateway *pasarela
	mailer  *correo
	svc     *decision.Service
	worker  *decision.Worker
	claimID string
}

func nuevo(t *testing.T, n ports.PolicyTermsReader) *escenario {
	t.Helper()
	e := &escenario{cases: memory.NewCaseStore(), reloj: &reloj{now: inicio}, gateway: &pasarela{}, mailer: &correo{}}
	e.store = memory.NewDecisionStore(e.cases)
	c, _, err := e.cases.CreateWeb(context.Background(), domain.ClaimRecord{
		Claim: domain.Claim{ID: "cl-1", Subject: "sub-carolina", FilingKey: "k1", PolicyID: "HG-2025-880231",
			IncidentType: domain.IncidentWaterDamage, OccurredAt: inicio.Add(-48 * time.Hour), Place: "Calle 1",
			Description: "Filtración.", Status: domain.StatusActive, FiledAt: inicio.Add(-24 * time.Hour)},
		Channel: domain.ChannelWeb,
	})
	require.NoError(t, err)
	e.claimID = c.ID
	e.worker, err = decision.NewWorker(e.store, e.gateway, e.mailer, e.reloj, decision.WorkerConfig{})
	require.NoError(t, err)
	e.svc = decision.NewService(e.cases, e.store, n, e.reloj, e.worker.Notify)
	return e
}

func (e *escenario) estado(t *testing.T) domain.Status {
	t.Helper()
	c, err := e.cases.Get(context.Background(), e.claimID)
	require.NoError(t, err)
	return c.Status
}

func TestApprovingPaysTheIndemnityOnce(t *testing.T) {
	e := nuevo(t, nucleoDeCarolina())
	ctx := context.Background()

	d, err := e.svc.Approve(ctx, e.claimID, "analista", 435_000_000)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusApproved, e.estado(t))
	assert.EqualValues(t, 413_250_000, d.Payout.AmountMinor)

	e.worker.ProcessDue(ctx)
	e.worker.ProcessDue(ctx)

	assert.Equal(t, domain.StatusPaid, e.estado(t))
	require.Len(t, e.gateway.calls, 1, "a paid claim is not paid again")
	sent := e.gateway.calls[0]
	assert.Equal(t, domain.PayoutKey(e.claimID), sent.Payout.Key)
	assert.Equal(t, cuenta, sent.Payout.Destination)
	got, err := e.svc.Decision(ctx, e.claimID)
	require.NoError(t, err)
	assert.Equal(t, "po-"+e.claimID, got.Payout.Reference)

	_, err = e.svc.Approve(ctx, e.claimID, "otro", 1)
	assert.ErrorIs(t, err, ports.ErrNotDecidable, "a decided claim is not decided again")
}

func TestARejectedPayoutIsAPaymentExceptionAndTheRetryPaysTheCorrectedAccount(t *testing.T) {
	e := nuevo(t, nucleoDeCarolina())
	ctx := context.Background()
	e.gateway.answer = func(d domain.Decision) error {
		if d.Payout.Destination.AccountNumber == cuenta.AccountNumber {
			return &payments.RejectedError{StatusCode: 422, Code: "invalid_destination_account", Operation: "payout"}
		}
		return nil
	}
	_, err := e.svc.Approve(ctx, e.claimID, "analista", 435_000_000)
	require.NoError(t, err)

	e.worker.ProcessDue(ctx)

	assert.Equal(t, domain.StatusPaymentException, e.estado(t))
	d, err := e.svc.Decision(ctx, e.claimID)
	require.NoError(t, err)
	assert.Equal(t, domain.PayoutRejected, d.Payout.Failure)
	assert.Equal(t, "invalid_destination_account", d.Payout.GatewayCode)
	assert.Equal(t, 435_000_000, int(d.GrossAmountMinor), "the approval is kept")
	require.Len(t, e.gateway.calls, 1, "a rejection is final: not retried automatically")

	corregida := cuenta
	corregida.AccountNumber = "0123-4567-12"
	_, err = e.svc.RetryPayout(ctx, e.claimID, "analista", &corregida)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusApproved, e.estado(t))
	e.worker.ProcessDue(ctx)

	assert.Equal(t, domain.StatusPaid, e.estado(t))
	require.Len(t, e.gateway.calls, 2)
	assert.Equal(t, e.gateway.calls[0].Payout.Key, e.gateway.calls[1].Payout.Key, "the retry keeps the key")
	assert.Equal(t, corregida, e.gateway.calls[1].Payout.Destination)
	assert.Len(t, e.gateway.pagados, 1, "one approval, one disbursement")

	_, err = e.svc.RetryPayout(ctx, e.claimID, "analista", nil)
	assert.ErrorIs(t, err, ports.ErrPayoutNotRetryable, "a paid claim is not retried")
}

func TestAGatewayThatDoesNotAnswerIsRetriedThreeTimesWithTheSameKey(t *testing.T) {
	e := nuevo(t, nucleoDeCarolina())
	ctx := context.Background()
	caida := true
	e.gateway.answer = func(domain.Decision) error {
		if caida {
			return payments.ErrAmbiguous
		}
		return nil
	}
	_, err := e.svc.Approve(ctx, e.claimID, "analista", 435_000_000)
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		e.worker.ProcessDue(ctx)
		e.reloj.avanzar(time.Minute)
	}

	require.Len(t, e.gateway.calls, domain.MaxAutomaticPayoutAttempts)
	for _, c := range e.gateway.calls {
		assert.Equal(t, domain.PayoutKey(e.claimID), c.Payout.Key)
	}
	assert.Equal(t, domain.StatusPaymentException, e.estado(t))
	d, err := e.svc.Decision(ctx, e.claimID)
	require.NoError(t, err)
	assert.Equal(t, domain.PayoutRetriesExhausted, d.Payout.Failure)
	assert.Equal(t, domain.MaxAutomaticPayoutAttempts, d.Payout.Attempts)

	caida = false
	_, err = e.svc.RetryPayout(ctx, e.claimID, "analista", nil)
	require.NoError(t, err)
	e.worker.ProcessDue(ctx)
	assert.Equal(t, domain.StatusPaid, e.estado(t))
	assert.Equal(t, cuenta, e.gateway.calls[len(e.gateway.calls)-1].Payout.Destination, "the same destination")
}

func TestAnApprovalIsCheckedAgainstTheCoverageOfToday(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		nucleo ports.PolicyTermsReader
		amount int64
		want   error
	}{
		"no policy core": {nil, 1, decision.ErrPolicyUnavailable},
		"core down":      {nucleo{errTerms: ports.ErrPolicyDirectoryUnavailable}, 1, decision.ErrPolicyUnavailable},
		"holder down":    {func() nucleo { n := nucleoDeCarolina(); n.errHolder = errors.New("x"); return n }(), 1, decision.ErrPolicyUnavailable},
		"policy unknown": {nucleo{errTerms: ports.ErrPolicyNotFound}, 1, decision.ErrNotCovered},
		"incident not covered": {func() nucleo {
			n := nucleoDeCarolina()
			n.terms.Coverages = []domain.Coverage{{Incident: domain.IncidentTheft, InsuredValueMinor: 1}}
			return n
		}(), 1, decision.ErrNotCovered},
		"over the insured value": {nucleoDeCarolina(), agua.InsuredValueMinor + 1, domain.ErrAmountExceedsInsuredValue},
	} {
		e := nuevo(t, tc.nucleo)
		_, err := e.svc.Approve(ctx, e.claimID, "analista", tc.amount)
		assert.ErrorIs(t, err, tc.want, name)
		assert.Equal(t, domain.StatusActive, e.estado(t), "%s: nothing was decided", name)
	}
	e := nuevo(t, nucleoDeCarolina())
	_, err := e.svc.Approve(ctx, "no-existe", "analista", 1)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestARejectionIsNotifiedToTheInsuredWithItsReason(t *testing.T) {
	e := nuevo(t, nucleoDeCarolina())
	ctx := context.Background()
	e.mailer.err = errors.New("proveedor caído")

	d, err := e.svc.Reject(ctx, e.claimID, "analista", "La póliza excluye daños por desgaste.")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRejected, e.estado(t))
	assert.Equal(t, "analista", d.DecidedBy)

	e.worker.ProcessDue(ctx)
	assert.Empty(t, e.mailer.sent)
	e.worker.ProcessDue(ctx)
	assert.Equal(t, 1, e.mailer.calls, "a failed notice waits for its next attempt")

	e.mailer.err = nil
	e.reloj.avanzar(time.Minute)
	e.worker.ProcessDue(ctx)
	e.reloj.avanzar(time.Minute)
	e.worker.ProcessDue(ctx)

	require.Len(t, e.mailer.sent, 1, "a sent notice is not sent again")
	assert.Contains(t, e.mailer.sent[0], "carolina@correo.example|")
	assert.Contains(t, e.mailer.sent[0], "La póliza excluye daños por desgaste.")
	assert.Contains(t, e.mailer.sent[0], "|rechazo-"+e.claimID)
	assert.Empty(t, e.gateway.calls, "a rejection pays nothing")
}

func TestARejectionWithoutReasonIsRefusedBeforeAskingTheCore(t *testing.T) {
	e := nuevo(t, nucleo{errTerms: errors.New("no debería llamarse"), errHolder: errors.New("x")})
	_, err := e.svc.Reject(context.Background(), e.claimID, "analista", "  ")
	assert.ErrorIs(t, err, domain.ErrReasonRequired)
	assert.Equal(t, domain.StatusActive, e.estado(t))
}

func TestWorkersRacingForAPayoutPayItOnce(t *testing.T) {
	e := nuevo(t, nucleoDeCarolina())
	ctx := context.Background()
	_, err := e.svc.Approve(ctx, e.claimID, "analista", 435_000_000)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		w, err := decision.NewWorker(e.store, e.gateway, nil, e.reloj, decision.WorkerConfig{})
		require.NoError(t, err)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.ProcessDue(ctx)
		}()
	}
	wg.Wait()
	assert.Len(t, e.gateway.calls, 1)
	assert.Equal(t, domain.StatusPaid, e.estado(t))
}

func TestTheNoticeOfRejectionNamesTheClaimAndTheReason(t *testing.T) {
	subject, body := decision.RejectionNotice(domain.Decision{FilingNumber: "SIN-2026-01655", Reason: "Fuera de vigencia."})
	assert.Equal(t, "Tu siniestro SIN-2026-01655 fue rechazado", subject)
	assert.Contains(t, body, "Motivo: Fuera de vigencia.")
}
