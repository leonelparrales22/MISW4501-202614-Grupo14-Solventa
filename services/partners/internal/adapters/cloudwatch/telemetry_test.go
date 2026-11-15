package cloudwatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/cloudwatch"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// HU-W3.2-03: the SLO of the partner API read from CloudWatch.

const prefix = "solventa-stg-slo-"

var (
	start = time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC)
	end   = time.Date(2026, 9, 17, 3, 30, 0, 0, time.UTC)
)

type fakeAPI struct {
	alarmPages   []*sdk.DescribeAlarmsOutput
	history      map[string][]types.AlarmHistoryItem
	metrics      map[string][]float64
	err          error
	alarmsIn     []*sdk.DescribeAlarmsInput
	historyIn    []*sdk.DescribeAlarmHistoryInput
	metricDataIn *sdk.GetMetricDataInput
}

func (f *fakeAPI) DescribeAlarms(_ context.Context, in *sdk.DescribeAlarmsInput, _ ...func(*sdk.Options)) (*sdk.DescribeAlarmsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.alarmsIn = append(f.alarmsIn, in)
	page := len(f.alarmsIn) - 1
	return f.alarmPages[page], nil
}

func (f *fakeAPI) DescribeAlarmHistory(_ context.Context, in *sdk.DescribeAlarmHistoryInput, _ ...func(*sdk.Options)) (*sdk.DescribeAlarmHistoryOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.historyIn = append(f.historyIn, in)
	return &sdk.DescribeAlarmHistoryOutput{AlarmHistoryItems: f.history[aws.ToString(in.AlarmName)]}, nil
}

func (f *fakeAPI) GetMetricData(_ context.Context, in *sdk.GetMetricDataInput, _ ...func(*sdk.Options)) (*sdk.GetMetricDataOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.metricDataIn = in
	out := &sdk.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		out.MetricDataResults = append(out.MetricDataResults, types.MetricDataResult{Id: q.Id, Values: f.metrics[aws.ToString(q.Id)]})
	}
	return out, nil
}

func dims(method, resource string) []types.Dimension {
	return []types.Dimension{
		{Name: aws.String("ApiName"), Value: aws.String("solventa-stg")},
		{Name: aws.String("Stage"), Value: aws.String("staging")},
		{Name: aws.String("Resource"), Value: aws.String(resource)},
		{Name: aws.String("Method"), Value: aws.String(method)},
	}
}

func metricAlarm(name string, state types.StateValue, threshold float64, d []types.Dimension) types.MetricAlarm {
	return types.MetricAlarm{
		AlarmName: aws.String(name), StateValue: state, Threshold: aws.Float64(threshold), Dimensions: d,
		StateUpdatedTimestamp: aws.Time(end.Add(-42 * time.Minute)),
	}
}

func newTelemetry(t *testing.T, api *fakeAPI) *cloudwatch.Telemetry {
	t.Helper()
	tel, err := cloudwatch.New(api, prefix)
	require.NoError(t, err)
	return tel
}

func TestTheAlarmsAreTheSLOOfEachRoute(t *testing.T) {
	api := &fakeAPI{alarmPages: []*sdk.DescribeAlarmsOutput{
		{MetricAlarms: []types.MetricAlarm{
			metricAlarm(prefix+"latency-post-offers", types.StateValueAlarm, 400, dims("POST", "/v1/offers")),
			metricAlarm(prefix+"errors-post-offers", types.StateValueOk, 0.05, dims("POST", "/v1/offers")),
		}, NextToken: aws.String("page-2")},
		{MetricAlarms: []types.MetricAlarm{
			metricAlarm(prefix+"other-thing", types.StateValueAlarm, 1, dims("POST", "/v1/offers")),
			metricAlarm(prefix+"errors-no-route", types.StateValueAlarm, 1, nil),
		}},
	}}

	alarms, err := newTelemetry(t, api).Alarms(context.Background())

	require.NoError(t, err)
	require.Len(t, alarms, 2, "only the two kinds of SLO alarm with a route")
	assert.Equal(t, domain.IncidentDegraded, alarms[0].Kind)
	assert.True(t, alarms[0].Active)
	assert.Equal(t, "POST /v1/offers", alarms[0].Route)
	assert.Equal(t, 400.0, alarms[0].Threshold)
	assert.Equal(t, end.Add(-42*time.Minute), alarms[0].Since)
	assert.Equal(t, domain.IncidentUnavailable, alarms[1].Kind)
	assert.False(t, alarms[1].Active)
	assert.Equal(t, prefix, aws.ToString(api.alarmsIn[0].AlarmNamePrefix))
	assert.Equal(t, "page-2", aws.ToString(api.alarmsIn[1].NextToken))
}

func TestTheHistoryKeepsOnlyTheEntriesAndExitsOfALARM(t *testing.T) {
	item := func(from, to string, at time.Time) types.AlarmHistoryItem {
		return types.AlarmHistoryItem{Timestamp: aws.Time(at),
			HistoryData: aws.String(`{"oldState":{"stateValue":"` + from + `"},"newState":{"stateValue":"` + to + `"}}`)}
	}
	name := prefix + "errors-post-offers"
	api := &fakeAPI{history: map[string][]types.AlarmHistoryItem{name: {
		item("OK", "ALARM", start.Add(time.Hour)),
		item("ALARM", "OK", start.Add(time.Hour+14*time.Minute)),
		item("INSUFFICIENT_DATA", "OK", start.Add(2*time.Hour)),
		{Timestamp: aws.Time(start), HistoryData: aws.String("not json")},
	}}}
	since := end.Add(-30 * 24 * time.Hour)

	got, err := newTelemetry(t, api).History(context.Background(), []domain.SLOAlarm{{Name: name}}, since)

	require.NoError(t, err)
	assert.Equal(t, []domain.AlarmTransition{
		{Alarm: name, Active: true, At: start.Add(time.Hour)},
		{Alarm: name, Active: false, At: start.Add(time.Hour + 14*time.Minute)},
	}, got)
	assert.Equal(t, types.HistoryItemTypeStateUpdate, api.historyIn[0].HistoryItemType)
	assert.Equal(t, since, aws.ToTime(api.historyIn[0].StartDate))
}

func TestTheTrafficAddsTheRoutesAndTakesTheSlowestP95(t *testing.T) {
	api := &fakeAPI{metrics: map[string][]float64{
		"count0": {8000}, "errors0": {3}, "p950": {360},
		"count1": {2000}, "errors1": {1}, "p951": {410},
		"count2": {0}, "errors2": {0}, "p952": {900}, // a route without requests does not count for the p95
	}}
	alarms := []domain.SLOAlarm{
		{Route: "POST /v1/offers", Dimensions: map[string]string{"ApiName": "solventa-stg", "Stage": "staging", "Resource": "/v1/offers", "Method": "POST"}},
		{Route: "POST /v1/offers", Dimensions: map[string]string{"Resource": "/v1/offers", "Method": "POST"}}, // same route, read once
		{Route: "POST /v1/audit/consents", Dimensions: map[string]string{"Resource": "/v1/audit/consents", "Method": "POST"}},
		{Route: "GET /v1/audit/consent-terms/current", Dimensions: map[string]string{"Resource": "/v1/audit/consent-terms/current", "Method": "GET"}},
	}

	got, err := newTelemetry(t, api).Traffic(context.Background(), alarms, start, end)

	require.NoError(t, err)
	assert.Equal(t, int64(10_000), got.Requests)
	assert.Equal(t, int64(4), got.ServerErrors)
	require.NotNil(t, got.P95Ms)
	assert.Equal(t, 410.0, *got.P95Ms)
	in := api.metricDataIn
	require.Len(t, in.MetricDataQueries, 9, "three metrics per route")
	assert.Equal(t, start, aws.ToTime(in.StartTime))
	q := in.MetricDataQueries[2]
	assert.Equal(t, "Latency", aws.ToString(q.MetricStat.Metric.MetricName))
	assert.Equal(t, "p95", aws.ToString(q.MetricStat.Stat))
	assert.Equal(t, int32(167*3600), aws.ToInt32(q.MetricStat.Period), "one datapoint: the window in whole hours")
	assert.Equal(t, "AWS/ApiGateway", aws.ToString(q.MetricStat.Metric.Namespace))
	assert.Len(t, q.MetricStat.Metric.Dimensions, 4)
}

func TestWithoutRequestsThereIsNoP95(t *testing.T) {
	api := &fakeAPI{metrics: map[string][]float64{}}
	alarms := []domain.SLOAlarm{{Route: "POST /v1/offers", Dimensions: map[string]string{"Resource": "/v1/offers", "Method": "POST"}}}

	got, err := newTelemetry(t, api).Traffic(context.Background(), alarms, start, end)

	require.NoError(t, err)
	assert.Zero(t, got.Requests)
	assert.Nil(t, got.P95Ms)

	got, err = newTelemetry(t, api).Traffic(context.Background(), nil, start, end)
	require.NoError(t, err)
	assert.Equal(t, domain.Traffic{}, got)
}

func TestAFailureOfCloudWatchIsTelemetryUnavailable(t *testing.T) {
	tel := newTelemetry(t, &fakeAPI{err: errors.New("throttled")})
	alarms := []domain.SLOAlarm{{Name: "a", Route: "POST /v1/offers", Dimensions: map[string]string{"Method": "POST"}}}

	_, err := tel.Alarms(context.Background())
	assert.ErrorIs(t, err, domain.ErrTelemetryUnavailable)
	_, err = tel.History(context.Background(), alarms, start)
	assert.ErrorIs(t, err, domain.ErrTelemetryUnavailable)
	_, err = tel.Traffic(context.Background(), alarms, start, end)
	assert.ErrorIs(t, err, domain.ErrTelemetryUnavailable)
}

func TestTheAdapterNeedsTheClientAndThePrefix(t *testing.T) {
	_, err := cloudwatch.New(nil, prefix)
	assert.Error(t, err)
	_, err = cloudwatch.New(&fakeAPI{}, " ")
	assert.Error(t, err)
}
