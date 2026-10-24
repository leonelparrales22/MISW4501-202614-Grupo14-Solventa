package main

import (
	"context"
	"fmt"
	"log"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	cognitoadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/cognito"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Environment of the sign-up of the insured (HU-M1.1-01).
const (
	envCognitoUserPoolID = "COGNITO_USER_POOL_ID"
	envCognitoGroup      = "COGNITO_INSURED_GROUP"
)

// defaultInsuredGroup is the group every account created here joins; it is the
// role the token announces in cognito:groups.
const defaultInsuredGroup = "insured"

type registrationConfig struct {
	enabled bool
	poolID  string
	group   string
}

// loadRegistrationConfig reads the user pool of the sign-up. Without
// COGNITO_USER_POOL_ID the route is not mounted.
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
	return nil
}

// newRegistrationService wires the sign-up of the insured over the customer
// repository, the document repository where the two faces of the identity
// document are kept and the Cognito user pool. documentsAt is where those
// faces go, for the log. It returns nil when the pool is not configured, and
// the route is not mounted.
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
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for the user pool: %w", err)
	}
	provider := cognitoadapter.NewProvider(cip.NewFromConfig(awsCfg), cfg.poolID, cfg.group)
	log.Printf("sign-up of the insured: user pool %s, group %s, identity documents in %s",
		cfg.poolID, cfg.group, documentsAt)
	return orchestrator.NewRegistrationService(customers, documents, provider, systemClock{}), nil
}
