// Package apigateway implements ports.Gateway over the Amazon API Gateway
// management API (HU-W3.1-01): one usage plan per partner, bound to the stage
// of the platform's REST API, with a daily quota and a rate limit, and one
// API key per credential. The gateway enforces the quota and answers 429 to
// the partner that exceeds it without touching the others (CA-2).
package apigateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// API is the part of the SDK client the adapter uses.
type API interface {
	CreateUsagePlan(ctx context.Context, in *sdk.CreateUsagePlanInput, opts ...func(*sdk.Options)) (*sdk.CreateUsagePlanOutput, error)
	UpdateUsagePlan(ctx context.Context, in *sdk.UpdateUsagePlanInput, opts ...func(*sdk.Options)) (*sdk.UpdateUsagePlanOutput, error)
	DeleteUsagePlan(ctx context.Context, in *sdk.DeleteUsagePlanInput, opts ...func(*sdk.Options)) (*sdk.DeleteUsagePlanOutput, error)
	CreateApiKey(ctx context.Context, in *sdk.CreateApiKeyInput, opts ...func(*sdk.Options)) (*sdk.CreateApiKeyOutput, error)
	DeleteApiKey(ctx context.Context, in *sdk.DeleteApiKeyInput, opts ...func(*sdk.Options)) (*sdk.DeleteApiKeyOutput, error)
	CreateUsagePlanKey(ctx context.Context, in *sdk.CreateUsagePlanKeyInput, opts ...func(*sdk.Options)) (*sdk.CreateUsagePlanKeyOutput, error)
	DeleteUsagePlanKey(ctx context.Context, in *sdk.DeleteUsagePlanKeyInput, opts ...func(*sdk.Options)) (*sdk.DeleteUsagePlanKeyOutput, error)
	GetUsage(ctx context.Context, in *sdk.GetUsageInput, opts ...func(*sdk.Options)) (*sdk.GetUsageOutput, error)
}

// Gateway is the adapter.
type Gateway struct {
	api   API
	apiID string
	stage string
}

var _ ports.Gateway = (*Gateway)(nil)

// New builds the adapter for the stage of a REST API.
func New(api API, restAPIID, stage string) (*Gateway, error) {
	if api == nil || restAPIID == "" || stage == "" {
		return nil, errors.New("apigateway: the client, the REST API id and the stage are required")
	}
	return &Gateway{api: api, apiID: restAPIID, stage: stage}, nil
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", domain.ErrGatewayUnavailable, op, err)
}

func notFound(err error) bool {
	var nf *types.NotFoundException
	return errors.As(err, &nf)
}

// CreateUsagePlan implements ports.Gateway.
func (g *Gateway) CreateUsagePlan(ctx context.Context, spec ports.UsagePlanSpec) (string, error) {
	out, err := g.api.CreateUsagePlan(ctx, &sdk.CreateUsagePlanInput{
		Name:        aws.String(spec.Name),
		Description: aws.String(spec.Description),
		ApiStages:   []types.ApiStage{{ApiId: aws.String(g.apiID), Stage: aws.String(g.stage)}},
		Quota:       &types.QuotaSettings{Limit: int32(spec.Quota.DailyLimit), Period: types.QuotaPeriodTypeDay}, //nolint:gosec // bounded by domain.MaxDailyLimit
		Throttle: &types.ThrottleSettings{
			RateLimit:  float64(spec.Quota.RateLimit),
			BurstLimit: int32(spec.Quota.BurstLimit), //nolint:gosec // bounded by domain.MaxBurstLimit
		},
	})
	if err != nil {
		return "", unavailable("create the usage plan", err)
	}
	return aws.ToString(out.Id), nil
}

// DeleteUsagePlan implements ports.Gateway. A usage plan bound to a stage
// cannot be deleted, so the stage is removed first.
func (g *Gateway) DeleteUsagePlan(ctx context.Context, planID string) error {
	_, err := g.api.UpdateUsagePlan(ctx, &sdk.UpdateUsagePlanInput{
		UsagePlanId: aws.String(planID),
		PatchOperations: []types.PatchOperation{{
			Op: types.OpRemove, Path: aws.String("/apiStages"), Value: aws.String(g.apiID + ":" + g.stage),
		}},
	})
	if err != nil && !notFound(err) {
		return unavailable("unbind the usage plan", err)
	}
	if _, err := g.api.DeleteUsagePlan(ctx, &sdk.DeleteUsagePlanInput{UsagePlanId: aws.String(planID)}); err != nil && !notFound(err) {
		return unavailable("delete the usage plan", err)
	}
	return nil
}

// CreateAPIKey implements ports.Gateway.
func (g *Gateway) CreateAPIKey(ctx context.Context, spec ports.APIKeySpec) (string, error) {
	out, err := g.api.CreateApiKey(ctx, &sdk.CreateApiKeyInput{
		Name: aws.String(spec.Name), Value: aws.String(spec.Value), Description: aws.String(spec.Description), Enabled: true,
	})
	if err != nil {
		return "", unavailable("create the api key", err)
	}
	return aws.ToString(out.Id), nil
}

// DeleteAPIKey implements ports.Gateway.
func (g *Gateway) DeleteAPIKey(ctx context.Context, keyID string) error {
	if _, err := g.api.DeleteApiKey(ctx, &sdk.DeleteApiKeyInput{ApiKey: aws.String(keyID)}); err != nil && !notFound(err) {
		return unavailable("delete the api key", err)
	}
	return nil
}

// AttachKey implements ports.Gateway.
func (g *Gateway) AttachKey(ctx context.Context, planID, keyID string) error {
	if _, err := g.api.CreateUsagePlanKey(ctx, &sdk.CreateUsagePlanKeyInput{
		UsagePlanId: aws.String(planID), KeyId: aws.String(keyID), KeyType: aws.String("API_KEY"),
	}); err != nil {
		return unavailable("attach the api key", err)
	}
	return nil
}

// DetachKey implements ports.Gateway.
func (g *Gateway) DetachKey(ctx context.Context, planID, keyID string) error {
	if _, err := g.api.DeleteUsagePlanKey(ctx, &sdk.DeleteUsagePlanKeyInput{
		UsagePlanId: aws.String(planID), KeyId: aws.String(keyID),
	}); err != nil && !notFound(err) {
		return unavailable("detach the api key", err)
	}
	return nil
}

// UsedOn implements ports.Gateway. GetUsage answers, per key, one
// [used, remaining] pair per day of the range.
func (g *Gateway) UsedOn(ctx context.Context, planID, day string) (map[string]int64, error) {
	out, err := g.api.GetUsage(ctx, &sdk.GetUsageInput{
		UsagePlanId: aws.String(planID), StartDate: aws.String(day), EndDate: aws.String(day),
	})
	if err != nil {
		return nil, unavailable("read the usage", err)
	}
	used := make(map[string]int64, len(out.Items))
	for key, days := range out.Items {
		for _, pair := range days {
			if len(pair) > 0 {
				used[key] += pair[0]
			}
		}
	}
	return used, nil
}
