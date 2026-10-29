package main

import (
	"context"
	"fmt"
	"log"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	cognitoadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/cognito"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Environment of the sign-up of the insured (HU-M1.1-01).
const (
	envCognitoUserPoolID = "COGNITO_USER_POOL_ID"
	envCognitoGroup      = "COGNITO_INSURED_GROUP"
	envKYCURL            = "KYC_URL"
)

// defaultInsuredGroup is the group every account created here joins; it is the
// role the token announces in cognito:groups.
const defaultInsuredGroup = "insured"

// identityBreaker isolates the identity provider of the sign-up. It is not the
// breaker of the profiling: a provider that is slow reading documents must not
// open the circuit of the quotes, nor the other way round.
var identityBreaker = resilience.Config{ConsecutiveFailures: 5, OpenTimeout: 30 * time.Second, HalfOpenSuccesses: 1}

type registrationConfig struct {
	enabled bool
	poolID  string
	group   string
	// kycURL is the identity provider that verifies the person before the
	// account is created (criterion 5).
	kycURL string
}

// loadRegistrationConfig reads the user pool of the sign-up. Without
// COGNITO_USER_POOL_ID the route is not mounted. With it, KYC_URL is required:
// signing people up without verifying them is what HU-M1.1-01 forbids, so the
// service refuses to start rather than create unverified accounts.
func loadRegistrationConfig(getenv func(string) string, cfg *registrationConfig) error {
	cfg.poolID = getenv(envCognitoUserPoolID)
	if cfg.poolID == "" {
		return nil
	}
	cfg.enabled = true
	cfg.group = getenv(envCognitoGroup)
	if cfg.group == "" {
		cfg.group = defaultInsuredGroup
	}
	cfg.kycURL = getenv(envKYCURL)
	if cfg.kycURL == "" {
		return fmt.Errorf("%s is required when %s is set: the identity of the insured is verified before the account is created",
			envKYCURL, envCognitoUserPoolID)
	}
	return nil
}

// newRegistrationService wires the sign-up of the insured over the customer
// repository, the document repository where the two faces of the identity
// document are kept, the identity provider and the Cognito user pool.
// documentsAt is where those faces go, for the log. It returns nil when the
// pool is not configured, and the route is not mounted.
func newRegistrationService(ctx context.Context, cfg registrationConfig, customers ports.CustomerRepository,
	documents ports.DocumentStore, documentsAt string) (*orchestrator.RegistrationService, error) {
	if !cfg.enabled {
		log.Printf("%s is not set: the sign-up of the insured (HU-M1.1-01) is not mounted", envCognitoUserPoolID)
		return nil, nil
	}
	if documents == nil {
		// loadConfig already refuses this combination; the check keeps the
		// service from signing anybody up without keeping their document.
		return nil, fmt.Errorf("the sign-up of the insured needs the document repository: the faces of the identity document are kept there")
	}
	if cfg.kycURL == "" {
		// Same as above: loadConfig refuses it, and the check keeps the
		// service from creating an account nobody verified.
		return nil, fmt.Errorf("the sign-up of the insured needs the identity provider: the person is verified before the account is created")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for the user pool: %w", err)
	}
	provider := cognitoadapter.NewProvider(cip.NewFromConfig(awsCfg), cfg.poolID, cfg.group)
	verifier := kycadapter.NewVerifier(kyc.NewClient(kyc.Config{
		BaseURL: cfg.kycURL, Timeout: kyc.IdentityTimeout, Breaker: identityBreaker,
	}))
	log.Printf("sign-up of the insured: user pool %s, group %s, identity provider %s, identity documents in %s",
		cfg.poolID, cfg.group, cfg.kycURL, documentsAt)
	return orchestrator.NewRegistrationService(customers, documents, verifier, provider, systemClock{}), nil
}
