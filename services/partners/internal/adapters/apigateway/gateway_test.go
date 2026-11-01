package apigateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/apigateway"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

type fakeAPI struct {
	err      error
	plan     *sdk.CreateUsagePlanInput
	key      *sdk.CreateApiKeyInput
	planKey  *sdk.CreateUsagePlanKeyInput
	patch    *sdk.UpdateUsagePlanInput
	deleted  []string
	usageOut *sdk.GetUsageOutput
}

func (f *fakeAPI) CreateUsagePlan(_ context.Context, in *sdk.CreateUsagePlanInput, _ ...func(*sdk.Options)) (*sdk.CreateUsagePlanOutput, error) {
	f.plan = in
	return &sdk.CreateUsagePlanOutput{Id: aws.String("plan-1")}, f.err
}

func (f *fakeAPI) UpdateUsagePlan(_ context.Context, in *sdk.UpdateUsagePlanInput, _ ...func(*sdk.Options)) (*sdk.UpdateUsagePlanOutput, error) {
	f.patch = in
	return &sdk.UpdateUsagePlanOutput{}, f.err
}

func (f *fakeAPI) DeleteUsagePlan(_ context.Context, in *sdk.DeleteUsagePlanInput, _ ...func(*sdk.Options)) (*sdk.DeleteUsagePlanOutput, error) {
	f.deleted = append(f.deleted, "plan:"+aws.ToString(in.UsagePlanId))
	return &sdk.DeleteUsagePlanOutput{}, f.err
}

func (f *fakeAPI) CreateApiKey(_ context.Context, in *sdk.CreateApiKeyInput, _ ...func(*sdk.Options)) (*sdk.CreateApiKeyOutput, error) {
	f.key = in
	return &sdk.CreateApiKeyOutput{Id: aws.String("key-1")}, f.err
}

func (f *fakeAPI) DeleteApiKey(_ context.Context, in *sdk.DeleteApiKeyInput, _ ...func(*sdk.Options)) (*sdk.DeleteApiKeyOutput, error) {
	f.deleted = append(f.deleted, "key:"+aws.ToString(in.ApiKey))
	return &sdk.DeleteApiKeyOutput{}, f.err
}

func (f *fakeAPI) CreateUsagePlanKey(_ context.Context, in *sdk.CreateUsagePlanKeyInput, _ ...func(*sdk.Options)) (*sdk.CreateUsagePlanKeyOutput, error) {
	f.planKey = in
	return &sdk.CreateUsagePlanKeyOutput{}, f.err
}

func (f *fakeAPI) DeleteUsagePlanKey(_ context.Context, in *sdk.DeleteUsagePlanKeyInput, _ ...func(*sdk.Options)) (*sdk.DeleteUsagePlanKeyOutput, error) {
	f.deleted = append(f.deleted, "plankey:"+aws.ToString(in.KeyId))
	return &sdk.DeleteUsagePlanKeyOutput{}, f.err
}

func (f *fakeAPI) GetUsage(_ context.Context, _ *sdk.GetUsageInput, _ ...func(*sdk.Options)) (*sdk.GetUsageOutput, error) {
	return f.usageOut, f.err
}

func TestNewRequiresEverything(t *testing.T) {
	_, err := apigateway.New(nil, "api", "stage")
	require.Error(t, err)
	_, err = apigateway.New(&fakeAPI{}, "", "stage")
	require.Error(t, err)
}

func TestCreatesPlanWithDailyQuotaAndKey(t *testing.T) {
	api := &fakeAPI{}
	g, err := apigateway.New(api, "api-1", "v1")
	require.NoError(t, err)
	id, err := g.CreateUsagePlan(context.Background(), ports.UsagePlanSpec{Name: "n",
		Quota: domain.Quota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200}})
	require.NoError(t, err)
	assert.Equal(t, "plan-1", id)
	assert.Equal(t, types.QuotaPeriodTypeDay, api.plan.Quota.Period)
	assert.EqualValues(t, 5000, api.plan.Quota.Limit)
	assert.EqualValues(t, 100, api.plan.Throttle.RateLimit)
	assert.EqualValues(t, 200, api.plan.Throttle.BurstLimit)
	assert.Equal(t, "api-1", aws.ToString(api.plan.ApiStages[0].ApiId))

	key, err := g.CreateAPIKey(context.Background(), ports.APIKeySpec{Name: "sol_1", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, "key-1", key)
	assert.True(t, api.key.Enabled)
	require.NoError(t, g.AttachKey(context.Background(), "plan-1", "key-1"))
	assert.Equal(t, "API_KEY", aws.ToString(api.planKey.KeyType))

	require.NoError(t, g.DetachKey(context.Background(), "plan-1", "key-1"))
	require.NoError(t, g.DeleteAPIKey(context.Background(), "key-1"))
	require.NoError(t, g.DeleteUsagePlan(context.Background(), "plan-1"))
	assert.Equal(t, "api-1:v1", aws.ToString(api.patch.PatchOperations[0].Value))
	assert.Equal(t, []string{"plankey:key-1", "key:key-1", "plan:plan-1"}, api.deleted)
}

func TestUsedOnSumsTheDay(t *testing.T) {
	api := &fakeAPI{usageOut: &sdk.GetUsageOutput{Items: map[string][][]int64{"k1": {{3120, 1880}}, "k2": {{5, 1}, {}}}}}
	g, _ := apigateway.New(api, "api-1", "v1")
	used, err := g.UsedOn(context.Background(), "plan-1", "2026-11-01")
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"k1": 3120, "k2": 5}, used)
}

func TestErrors(t *testing.T) {
	api := &fakeAPI{err: errors.New("throttled by aws")}
	g, _ := apigateway.New(api, "api-1", "v1")
	ctx := context.Background()
	_, err := g.CreateUsagePlan(ctx, ports.UsagePlanSpec{})
	require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
	_, err = g.CreateAPIKey(ctx, ports.APIKeySpec{})
	require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
	require.ErrorIs(t, g.AttachKey(ctx, "p", "k"), domain.ErrGatewayUnavailable)
	require.ErrorIs(t, g.DetachKey(ctx, "p", "k"), domain.ErrGatewayUnavailable)
	require.ErrorIs(t, g.DeleteAPIKey(ctx, "k"), domain.ErrGatewayUnavailable)
	require.ErrorIs(t, g.DeleteUsagePlan(ctx, "p"), domain.ErrGatewayUnavailable)
	_, err = g.UsedOn(ctx, "p", "d")
	require.ErrorIs(t, err, domain.ErrGatewayUnavailable)

	// Deleting what no longer exists is not an error.
	api.err = &types.NotFoundException{Message: aws.String("gone")}
	require.NoError(t, g.DetachKey(ctx, "p", "k"))
	require.NoError(t, g.DeleteAPIKey(ctx, "k"))
	require.NoError(t, g.DeleteUsagePlan(ctx, "p"))
}
