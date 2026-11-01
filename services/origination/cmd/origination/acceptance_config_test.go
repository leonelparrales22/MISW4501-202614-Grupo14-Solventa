package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestAcceptanceRoutesAreDisabledWithoutAuth(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.False(t, cfg.acceptances.enabled)
	assert.Equal(t, time.Second, cfg.acceptances.processor.PollInterval)
	assert.Equal(t, 5*time.Second, cfg.acceptances.processor.AttemptTimeout)
	assert.Equal(t, 2*time.Second, cfg.acceptances.processor.RetryBase)
	assert.Equal(t, time.Minute, cfg.acceptances.processor.RetryMax)
	assert.Equal(t, 2, cfg.acceptances.flakyFailures)
}

func TestAcceptanceConfigWithAuthAndOverrides(t *testing.T) {
	m := requiredEnv()
	m["AUTH_ISSUER"] = "https://cognito-idp.us-east-2.amazonaws.com/pool"
	m["AUTH_CLIENT_IDS"] = "web,mobile"
	m["ACCEPTANCE_POLL_INTERVAL_MS"] = "500"
	m["ACCEPTANCE_ATTEMPT_TIMEOUT_MS"] = "1500"
	m["ACCEPTANCE_RETRY_BASE_MS"] = "1000"
	m["ACCEPTANCE_RETRY_MAX_MS"] = "8000"
	m["UNDERWRITING_STUB_FLAKY_FAILURES"] = "0"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	a := cfg.acceptances
	assert.True(t, a.enabled)
	assert.Equal(t, "https://cognito-idp.us-east-2.amazonaws.com/pool", a.auth.Issuer)
	assert.Equal(t, []string{"web", "mobile"}, a.auth.ClientIDs)
	assert.Equal(t, 500*time.Millisecond, a.processor.PollInterval)
	assert.Equal(t, 1500*time.Millisecond, a.processor.AttemptTimeout)
	assert.Equal(t, time.Second, a.processor.RetryBase)
	assert.Equal(t, 8*time.Second, a.processor.RetryMax)
	assert.Zero(t, a.flakyFailures)
}

func TestAcceptanceConfigRejectsInvalidValues(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"auth without clients": {"AUTH_ISSUER": "https://issuer"},
		"poll":                 {"ACCEPTANCE_POLL_INTERVAL_MS": "0"},
		"attempt":              {"ACCEPTANCE_ATTEMPT_TIMEOUT_MS": "x"},
		"base":                 {"ACCEPTANCE_RETRY_BASE_MS": "-1"},
		"max":                  {"ACCEPTANCE_RETRY_MAX_MS": "x"},
		"max below base":       {"ACCEPTANCE_RETRY_BASE_MS": "5000", "ACCEPTANCE_RETRY_MAX_MS": "1000"},
		"flaky":                {"UNDERWRITING_STUB_FLAKY_FAILURES": "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			m := requiredEnv()
			for k, v := range extra {
				m[k] = v
			}
			_, err := loadConfig(env(m))
			assert.Error(t, err)
		})
	}
}

func TestUnderwritingReviewScoreThreshold(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))
	require.NoError(t, err)
	assert.Equal(t, 0.8, cfg.acceptances.reviewScoreThreshold, "default when the variable is not set")

	for raw, want := range map[string]float64{"0.6": 0.6, "0.05": 0.05, "1": 1} {
		m := requiredEnv()
		m["UNDERWRITING_REVIEW_SCORE_THRESHOLD"] = raw
		cfg, err := loadConfig(env(m))
		require.NoError(t, err, raw)
		assert.Equal(t, want, cfg.acceptances.reviewScoreThreshold, raw)
	}

	for _, raw := range []string{"0", "-0.1", "1.01", "x", "NaN", "Inf"} {
		m := requiredEnv()
		m["UNDERWRITING_REVIEW_SCORE_THRESHOLD"] = raw
		_, err := loadConfig(env(m))
		assert.Error(t, err, raw)
	}
}

func TestNewAcceptanceServiceProcessesInTheBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, _ := newAcceptanceService(ctx, acceptanceConfig{flakyFailures: 0, reviewScoreThreshold: memory.DefaultReviewScoreThreshold}, memory.NewIssuanceStub(), nil)
	caller := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})

	a, created, err := svc.Accept(caller, "demo-active-1", domain.ChannelWeb)
	require.NoError(t, err)
	require.True(t, created)

	require.Eventually(t, func() bool {
		got, err := svc.Get(caller, a.ID)
		return err == nil && got.Status == domain.AcceptanceIssuancePending
	}, 3*time.Second, 20*time.Millisecond)
}

func TestNewAcceptanceServiceUsesTheReviewScoreThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// demo-active offers have a score of 0.72: above a threshold of 0.6 they
	// go to the advisor.
	svc, _ := newAcceptanceService(ctx, acceptanceConfig{reviewScoreThreshold: 0.6}, memory.NewIssuanceStub(), nil)
	caller := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})

	a, _, err := svc.Accept(caller, "demo-active-1", domain.ChannelWeb)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		got, err := svc.Get(caller, a.ID)
		return err == nil && got.Status == domain.AcceptanceInReview &&
			got.Decision != nil && got.Decision.Reason == domain.ReviewReasonHighRiskScore
	}, 3*time.Second, 20*time.Millisecond)
}

func TestContractDocumentIsOffWithoutEmailURL(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.False(t, cfg.contracts.enabled)
}

func contractEnv() map[string]string {
	m := requiredEnv()
	m["EMAIL_URL"] = "http://email:9030"
	m["DOCUMENTS_DIR"] = "/var/solventa/documents"
	m["DOCUMENT_LINK_SECRET"] = "0123456789abcdef0123456789abcdef"
	return m
}

func TestContractConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(contractEnv()))

	require.NoError(t, err)
	c := cfg.contracts
	assert.True(t, c.enabled)
	assert.Equal(t, "http://email:9030", c.emailURL)
	assert.Equal(t, "/var/solventa/documents", cfg.documents.dir)
	assert.Equal(t, 2*time.Second, c.emailTimeout)
	assert.Equal(t, 500*time.Millisecond, c.service.PollInterval)
	assert.Equal(t, 10*time.Second, c.service.AttemptTimeout)
	assert.Equal(t, time.Second, c.service.RetryBase)
	assert.Equal(t, 15*time.Second, c.service.RetryMax)
	assert.Equal(t, 12, c.service.MaxAttempts)
	assert.Equal(t, 7*24*time.Hour, c.service.LinkTTL)
	assert.Equal(t, "http://localhost:8080", c.service.PublicBaseURL)
}

func TestContractConfigOverrides(t *testing.T) {
	m := contractEnv()
	m["PORT"] = "9999"
	m["DOCUMENTS_PUBLIC_BASE_URL"] = "https://api.solventa.example"
	m["DOCUMENT_LINK_TTL_HOURS"] = "24"
	m["CONTRACT_POLL_INTERVAL_MS"] = "100"
	m["CONTRACT_ATTEMPT_TIMEOUT_MS"] = "3000"
	m["CONTRACT_RETRY_BASE_MS"] = "200"
	m["CONTRACT_RETRY_MAX_MS"] = "1600"
	m["CONTRACT_MAX_ATTEMPTS"] = "5"
	m["EMAIL_TIMEOUT_MS"] = "500"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	c := cfg.contracts
	assert.Equal(t, "https://api.solventa.example", c.service.PublicBaseURL)
	assert.Equal(t, 24*time.Hour, c.service.LinkTTL)
	assert.Equal(t, 100*time.Millisecond, c.service.PollInterval)
	assert.Equal(t, 3*time.Second, c.service.AttemptTimeout)
	assert.Equal(t, 200*time.Millisecond, c.service.RetryBase)
	assert.Equal(t, 1600*time.Millisecond, c.service.RetryMax)
	assert.Equal(t, 5, c.service.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, c.emailTimeout)
}

// TestContractConfigTakesExactlyOneDocumentStore covers decision D-3 a of
// HA-D02: with EMAIL_URL, the documents go to an S3 bucket (AWS) or to a local
// directory, never to both and never to neither.
func TestContractConfigTakesExactlyOneDocumentStore(t *testing.T) {
	t.Run("only the bucket", func(t *testing.T) {
		m := contractEnv()
		delete(m, "DOCUMENTS_DIR")
		m["DOCUMENTS_BUCKET"] = "solventa-stg-documents-368083581678"

		cfg, err := loadConfig(env(m))

		require.NoError(t, err)
		assert.True(t, cfg.contracts.enabled)
		assert.Equal(t, "solventa-stg-documents-368083581678", cfg.documents.bucket)
		assert.Empty(t, cfg.documents.dir)
	})
	t.Run("only the directory", func(t *testing.T) {
		cfg, err := loadConfig(env(contractEnv()))

		require.NoError(t, err)
		assert.Empty(t, cfg.documents.bucket)
		assert.Equal(t, "/var/solventa/documents", cfg.documents.dir)
	})
	t.Run("both", func(t *testing.T) {
		m := contractEnv()
		m["DOCUMENTS_BUCKET"] = "solventa-stg-documents-368083581678"

		_, err := loadConfig(env(m))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "DOCUMENTS_BUCKET and DOCUMENTS_DIR are both set")
	})
	t.Run("neither", func(t *testing.T) {
		m := contractEnv()
		delete(m, "DOCUMENTS_DIR")

		_, err := loadConfig(env(m))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "DOCUMENTS_BUCKET")
		assert.Contains(t, err.Error(), "DOCUMENTS_DIR")
	})
	// Sin EMAIL_URL y sin registro, el repositorio no es obligatorio: se lee
	// igual, porque el documento de identidad del registro (HU-M1.1-01) va al
	// mismo sitio, pero nadie lo exige.
	t.Run("without EMAIL_URL it is not required", func(t *testing.T) {
		m := requiredEnv()
		m["DOCUMENTS_BUCKET"] = "solventa-stg-documents-368083581678"

		cfg, err := loadConfig(env(m))

		require.NoError(t, err)
		assert.False(t, cfg.contracts.enabled)
		assert.Equal(t, "solventa-stg-documents-368083581678", cfg.documents.bucket)
	})
	// Nombrar dos repositorios es una contradicción, con EMAIL_URL o sin ella.
	t.Run("both without EMAIL_URL", func(t *testing.T) {
		m := requiredEnv()
		m["DOCUMENTS_BUCKET"] = "solventa-stg-documents-368083581678"
		m["DOCUMENTS_DIR"] = "/var/solventa/documents"

		_, err := loadConfig(env(m))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "DOCUMENTS_BUCKET and DOCUMENTS_DIR are both set")
	})
}

// isolateAWSConfig points the AWS SDK of the test at dummy credentials and
// at no shared configuration, so the developer's profile is never read.
func isolateAWSConfig(t *testing.T, endpoint string) {
	t.Helper()
	none := filepath.Join(t.TempDir(), "none")
	t.Setenv("AWS_CONFIG_FILE", none)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", none)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
}

func TestNewDocumentStoreRefusesAnUnreachableBucket(t *testing.T) {
	isolateAWSConfig(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := newDocumentStore(ctx, documentsConfig{bucket: "solventa-stg-documents"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"solventa-stg-documents" is not reachable`)
}

func TestNewDocumentStoreUsesTheBucketOfTheEnvironment(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := localstack.Run(ctx, "localstack/localstack:4.14.0", testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)
	if host == "localhost" {
		// An IP address makes the SDK address the bucket by path, as LocalStack needs.
		host = "127.0.0.1"
	}
	isolateAWSConfig(t, fmt.Sprintf("http://%s:%s", host, port.Port()))
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	_, err = awss3.NewFromConfig(awsCfg).CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket:                    aws.String("solventa-test-documents"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraintUsEast2},
	})
	require.NoError(t, err)

	store, where, err := newDocumentStore(ctx, documentsConfig{bucket: "solventa-test-documents"})

	require.NoError(t, err)
	assert.Equal(t, "s3://solventa-test-documents", where)
	require.NoError(t, store.Put(ctx, "policies/pol-1/contract-v1.pdf", []byte("%PDF-1"), "application/pdf"))
	got, err := store.Get(ctx, "policies/pol-1/contract-v1.pdf")
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1"), got)
}

func TestNewDocumentStoreFallsBackToTheDirectory(t *testing.T) {
	dir := t.TempDir()

	_, where, err := newDocumentStore(context.Background(), documentsConfig{dir: dir})

	require.NoError(t, err)
	abs, _ := filepath.Abs(dir)
	assert.Equal(t, abs, where)

	_, _, err = newDocumentStore(context.Background(), documentsConfig{})
	assert.Error(t, err, "sin bucket y sin directorio no hay repositorio")
}

func TestContractConfigRejectsInvalidValues(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"no documents dir":  {"DOCUMENTS_DIR": ""},
		"dir and bucket":    {"DOCUMENTS_BUCKET": "solventa-stg-documents"},
		"no link secret":    {"DOCUMENT_LINK_SECRET": ""},
		"short link secret": {"DOCUMENT_LINK_SECRET": "short"},
		"poll":              {"CONTRACT_POLL_INTERVAL_MS": "0"},
		"attempt":           {"CONTRACT_ATTEMPT_TIMEOUT_MS": "x"},
		"base":              {"CONTRACT_RETRY_BASE_MS": "-1"},
		"max":               {"CONTRACT_RETRY_MAX_MS": "x"},
		"max below base":    {"CONTRACT_RETRY_BASE_MS": "5000", "CONTRACT_RETRY_MAX_MS": "1000"},
		"max attempts":      {"CONTRACT_MAX_ATTEMPTS": "0"},
		"email timeout":     {"EMAIL_TIMEOUT_MS": "x"},
		"link ttl":          {"DOCUMENT_LINK_TTL_HOURS": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			m := contractEnv()
			for k, v := range extra {
				m[k] = v
			}
			_, err := loadConfig(env(m))
			assert.Error(t, err)
		})
	}
}

func TestNewContractWiringStartsTheWorkerAndServesTheDocument(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	email := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	defer email.Close()
	cfg, err := loadConfig(env(map[string]string{
		"REDIS_ADDR": "r:6379", "OPEN_FINANCE_URL": "http://of", "EMAIL_URL": email.URL,
		"DOCUMENTS_DIR": t.TempDir(), "DOCUMENT_LINK_SECRET": "0123456789abcdef0123456789abcdef", "CONTRACT_POLL_INTERVAL_MS": "10",
	}))
	require.NoError(t, err)

	store, where, err := newDocumentStore(ctx, cfg.documents)
	require.NoError(t, err)
	wiring, err := newContractWiring(ctx, cfg.contracts, store, where, nil)
	require.NoError(t, err)

	now := time.Now().UTC()
	// The activation the premium charge publishes once it is confirmed (HA-D02).
	require.NoError(t, wiring.service.Receive(ctx, domain.CoverageActivated{
		EventID: "evt-coverage-activated-acc-1", PolicyID: "pol-acc-1", AcceptanceID: "acc-1", CustomerID: "cust-1",
		Holder:  domain.Applicant{FullName: "Ana", DocumentID: "123", Email: "a@x.co"},
		Premium: 89_500, Currency: "COP", InsuredAmount: demoInsuredAmount,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: demoInsuredAmount},
			{Code: domain.CoverageDisability, InsuredAmount: demoInsuredAmount},
		},
		ActivatedAt: now, CoverageStart: now, CoverageEnd: now.AddDate(0, demoTermMonths, 0),
	}))
	caller := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})
	require.Eventually(t, func() bool {
		v, err := wiring.reader.Status(caller, "pol-acc-1")
		return err == nil && v.Issuance.Status == domain.ContractCompleted
	}, 5*time.Second, 20*time.Millisecond)
	assert.NotNil(t, wiring.emailClient, "shared with the review outcome notices (HU-W1.1-02)")
	doc, content, err := wiring.reader.Document(caller, "pol-acc-1")
	require.NoError(t, err)
	assert.Equal(t, 1, doc.Version)
	assert.True(t, len(content) > 0 && string(content[:4]) == "%PDF")
	assert.WithinDuration(t, now, doc.GeneratedAt, 5*time.Second)
}

func TestNewContractWiringRejectsABadSetup(t *testing.T) {
	store, where, err := newDocumentStore(context.Background(), documentsConfig{dir: t.TempDir()})
	require.NoError(t, err)

	_, err = newContractWiring(context.Background(), contractConfig{linkSecret: "short"}, store, where, nil)
	assert.Error(t, err, "short secret")
}
