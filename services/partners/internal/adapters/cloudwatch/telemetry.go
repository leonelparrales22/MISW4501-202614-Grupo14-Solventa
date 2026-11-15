// Package cloudwatch implements ports.Telemetry over Amazon CloudWatch
// (HU-W3.2-03). The SLO alarms of the partner API share a name prefix
// (<name>-slo-latency-<route> and <name>-slo-errors-<route>, created by the
// apigateway Terraform module); their dimensions say which route of the API
// Gateway each one watches, so the metrics of the dashboard are the same ones
// the SLO is measured with.
package cloudwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// API is the part of the SDK client the adapter uses.
type API interface {
	DescribeAlarms(ctx context.Context, in *sdk.DescribeAlarmsInput, opts ...func(*sdk.Options)) (*sdk.DescribeAlarmsOutput, error)
	DescribeAlarmHistory(ctx context.Context, in *sdk.DescribeAlarmHistoryInput, opts ...func(*sdk.Options)) (*sdk.DescribeAlarmHistoryOutput, error)
	GetMetricData(ctx context.Context, in *sdk.GetMetricDataInput, opts ...func(*sdk.Options)) (*sdk.GetMetricDataOutput, error)
}

// Kinds of SLO alarm, by the segment that follows the prefix in its name.
const (
	latencyKind = "latency-"
	errorsKind  = "errors-"
)

const namespace = "AWS/ApiGateway"

// maxPages bounds the pagination of one read, so a broken token never loops.
const maxPages = 20

// Telemetry is the adapter.
type Telemetry struct {
	api    API
	prefix string
}

var _ ports.Telemetry = (*Telemetry)(nil)

// New builds the adapter for the alarms whose name starts with prefix.
func New(api API, prefix string) (*Telemetry, error) {
	if api == nil || strings.TrimSpace(prefix) == "" {
		return nil, errors.New("cloudwatch: the client and the prefix of the SLO alarms are required")
	}
	return &Telemetry{api: api, prefix: prefix}, nil
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", domain.ErrTelemetryUnavailable, op, err)
}

// Alarms implements ports.Telemetry. Alarms with the prefix but none of the
// two kinds, or without a route, are ignored.
func (t *Telemetry) Alarms(ctx context.Context) ([]domain.SLOAlarm, error) {
	var out []domain.SLOAlarm
	var token *string
	for page := 0; page < maxPages; page++ {
		res, err := t.api.DescribeAlarms(ctx, &sdk.DescribeAlarmsInput{
			AlarmNamePrefix: aws.String(t.prefix), AlarmTypes: []types.AlarmType{types.AlarmTypeMetricAlarm}, NextToken: token,
		})
		if err != nil {
			return nil, unavailable("describe the SLO alarms", err)
		}
		for _, a := range res.MetricAlarms {
			if alarm, ok := t.alarmOf(a); ok {
				out = append(out, alarm)
			}
		}
		if token = res.NextToken; token == nil {
			break
		}
	}
	return out, nil
}

func (t *Telemetry) alarmOf(a types.MetricAlarm) (domain.SLOAlarm, bool) {
	name := aws.ToString(a.AlarmName)
	var kind domain.IncidentKind
	switch rest := strings.TrimPrefix(name, t.prefix); {
	case strings.HasPrefix(rest, latencyKind):
		kind = domain.IncidentDegraded
	case strings.HasPrefix(rest, errorsKind):
		kind = domain.IncidentUnavailable
	default:
		return domain.SLOAlarm{}, false
	}
	route := routeOf(a.Dimensions)
	if route == "" {
		return domain.SLOAlarm{}, false
	}
	dims := make(map[string]string, len(a.Dimensions))
	for _, d := range a.Dimensions {
		dims[aws.ToString(d.Name)] = aws.ToString(d.Value)
	}
	return domain.SLOAlarm{
		Name: name, Kind: kind, Route: route, Dimensions: dims, Threshold: aws.ToFloat64(a.Threshold),
		Active: a.StateValue == types.StateValueAlarm, Since: aws.ToTime(a.StateUpdatedTimestamp),
	}, true
}

func dimension(dims []types.Dimension, name string) string {
	for _, d := range dims {
		if aws.ToString(d.Name) == name {
			return aws.ToString(d.Value)
		}
	}
	return ""
}

func routeOf(dims []types.Dimension) string {
	method, resource := dimension(dims, "Method"), dimension(dims, "Resource")
	if method == "" || resource == "" {
		return ""
	}
	return method + " " + resource
}

// History implements ports.Telemetry: the changes of state of each alarm
// since since. CloudWatch has no prefix filter for the history, so it asks
// alarm by alarm (and the task role can only read these).
func (t *Telemetry) History(ctx context.Context, alarms []domain.SLOAlarm, since time.Time) ([]domain.AlarmTransition, error) {
	var out []domain.AlarmTransition
	for _, a := range alarms {
		var token *string
		for page := 0; page < maxPages; page++ {
			res, err := t.api.DescribeAlarmHistory(ctx, &sdk.DescribeAlarmHistoryInput{
				AlarmName: aws.String(a.Name), HistoryItemType: types.HistoryItemTypeStateUpdate,
				StartDate: aws.Time(since), NextToken: token,
			})
			if err != nil {
				return nil, unavailable("read the history of "+a.Name, err)
			}
			for _, item := range res.AlarmHistoryItems {
				if active, ok := enteredOrLeftAlarm(aws.ToString(item.HistoryData)); ok {
					out = append(out, domain.AlarmTransition{Alarm: a.Name, Active: active, At: aws.ToTime(item.Timestamp)})
				}
			}
			if token = res.NextToken; token == nil {
				break
			}
		}
	}
	return out, nil
}

// enteredOrLeftAlarm reads the HistoryData of a state update: true when the
// alarm entered ALARM, false when it left it. Other changes (OK and
// INSUFFICIENT_DATA between themselves) are not incidents.
func enteredOrLeftAlarm(data string) (active, ok bool) {
	var h struct {
		OldState struct {
			StateValue string `json:"stateValue"`
		} `json:"oldState"`
		NewState struct {
			StateValue string `json:"stateValue"`
		} `json:"newState"`
	}
	if json.Unmarshal([]byte(data), &h) != nil {
		return false, false
	}
	alarm := string(types.StateValueAlarm)
	switch {
	case h.NewState.StateValue == alarm && h.OldState.StateValue != alarm:
		return true, true
	case h.OldState.StateValue == alarm && h.NewState.StateValue != alarm:
		return false, true
	default:
		return false, false
	}
}

// Traffic implements ports.Telemetry: the requests and the 5XX answers of the
// routes of the alarms, added up, and the p95 latency of the slowest route
// with requests. Each route is read once with one datapoint for the whole
// window: its period is the window rounded up to whole hours, and every window
// starts at 00:00 of Colombia, a whole hour.
func (t *Telemetry) Traffic(ctx context.Context, alarms []domain.SLOAlarm, start, end time.Time) (domain.Traffic, error) {
	routes := routesOf(alarms)
	if len(routes) == 0 {
		return domain.Traffic{}, nil
	}
	period := int32(math.Ceil(end.Sub(start).Hours())) * 3600
	if period <= 0 {
		period = 3600
	}
	var queries []types.MetricDataQuery
	for i, dims := range routes {
		for _, q := range []struct{ id, metric, stat string }{
			{"count", "Count", "Sum"}, {"errors", "5XXError", "Sum"}, {"p95", "Latency", "p95"},
		} {
			queries = append(queries, types.MetricDataQuery{
				Id: aws.String(fmt.Sprintf("%s%d", q.id, i)), ReturnData: aws.Bool(true),
				MetricStat: &types.MetricStat{
					Metric: &types.Metric{Namespace: aws.String(namespace), MetricName: aws.String(q.metric), Dimensions: dims},
					Period: aws.Int32(period), Stat: aws.String(q.stat),
				},
			})
		}
	}
	values := map[string][]float64{}
	var token *string
	for page := 0; page < maxPages; page++ {
		res, err := t.api.GetMetricData(ctx, &sdk.GetMetricDataInput{
			MetricDataQueries: queries, StartTime: aws.Time(start), EndTime: aws.Time(start.Add(time.Duration(period) * time.Second)),
			NextToken: token,
		})
		if err != nil {
			return domain.Traffic{}, unavailable("read the metrics of the partner API", err)
		}
		for _, r := range res.MetricDataResults {
			values[aws.ToString(r.Id)] = append(values[aws.ToString(r.Id)], r.Values...)
		}
		if token = res.NextToken; token == nil {
			break
		}
	}
	var out domain.Traffic
	for i := range routes {
		count := sum(values[fmt.Sprintf("count%d", i)])
		out.Requests += int64(math.Round(count))
		out.ServerErrors += int64(math.Round(sum(values[fmt.Sprintf("errors%d", i)])))
		if p95 := values[fmt.Sprintf("p95%d", i)]; count > 0 && len(p95) > 0 {
			worst := maxOf(p95)
			if out.P95Ms == nil || worst > *out.P95Ms {
				out.P95Ms = &worst
			}
		}
	}
	return out, nil
}

// routesOf returns the dimensions of each route watched by the alarms, once.
func routesOf(alarms []domain.SLOAlarm) [][]types.Dimension {
	seen := map[string]bool{}
	var out [][]types.Dimension
	for _, a := range alarms {
		if seen[a.Route] {
			continue
		}
		seen[a.Route] = true
		names := make([]string, 0, len(a.Dimensions))
		for n := range a.Dimensions {
			names = append(names, n)
		}
		sort.Strings(names)
		dims := make([]types.Dimension, 0, len(names))
		for _, n := range names {
			dims = append(dims, types.Dimension{Name: aws.String(n), Value: aws.String(a.Dimensions[n])})
		}
		out = append(out, dims)
	}
	return out
}

func sum(vs []float64) float64 {
	total := 0.0
	for _, v := range vs {
		total += v
	}
	return total
}

func maxOf(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		m = math.Max(m, v)
	}
	return m
}
