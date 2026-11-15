# --- SLO alarms of the partner API (HU-W3.2-03) ---------------------------------------------
#
# The SLO of the integration of a partner is the one of the platform: latency
# p95 <= 400 ms on the quoting journey (HA-L01) and availability >= 99.9 %
# (RC-02), measured with CloudWatch (architecture decision on observability).
# Each partner route gets two alarms on its API Gateway metrics (detailed
# metrics are on for every method):
#   - latency: Latency p95 over the budget in two 5-minute periods in a row;
#   - errors:  5XXError average (the share of 5XX answers) at or over the
#              rate in three 1-minute periods in a row.
# An alarm that fires and returns to OK is one incident of the dashboard of
# the partner; the partners service reads them by the name prefix. The
# dimensions of each alarm say which route and which metrics make the SLO, so
# the service needs no list of its own.

locals {
  slo_routes = merge(
    {
      "POST /v1/risk-profiles/resolve"      = { method = "POST", resource = "/v1/risk-profiles/resolve" }
      "GET /v1/risk-profiles/{customer_id}" = { method = "GET", resource = "/v1/risk-profiles/{customer_id}" }
    },
    { for k, v in local.partner_methods : "${v.http_method} /v1/${v.route}" => { method = v.http_method, resource = "/v1/${v.route}" } },
  )

  # Alarm names: <name>-slo-<kind>-<method>-<path without /v1, / as ->, no braces>.
  slo_alarms = var.slo_alarms.enabled ? {
    for label, r in local.slo_routes :
    lower("${r.method}-${replace(replace(replace(trimprefix(r.resource, "/v1/"), "/", "-"), "{", ""), "}", "")}") => merge(r, { label = label })
  } : {}

  slo_alarm_prefix = "${var.name}-slo-"
}

resource "aws_cloudwatch_metric_alarm" "slo_latency" {
  for_each = local.slo_alarms

  alarm_name          = "${local.slo_alarm_prefix}latency-${each.key}"
  alarm_description   = "SLO of the partner API (HU-W3.2-03): p95 latency of ${each.value.label} over ${var.slo_alarms.latency_p95_ms} ms (degraded)."
  namespace           = "AWS/ApiGateway"
  metric_name         = "Latency"
  extended_statistic  = "p95"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = var.slo_alarms.latency_p95_ms
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  dimensions = {
    ApiName  = aws_api_gateway_rest_api.this.name
    Stage    = aws_api_gateway_stage.this.stage_name
    Resource = each.value.resource
    Method   = each.value.method
  }
  tags = var.tags
}

resource "aws_cloudwatch_metric_alarm" "slo_errors" {
  for_each = local.slo_alarms

  alarm_name          = "${local.slo_alarm_prefix}errors-${each.key}"
  alarm_description   = "SLO of the partner API (HU-W3.2-03): 5XX answers of ${each.value.label} at or over ${var.slo_alarms.error_rate * 100} % (unavailable)."
  namespace           = "AWS/ApiGateway"
  metric_name         = "5XXError"
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 3
  datapoints_to_alarm = 3
  threshold           = var.slo_alarms.error_rate
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions = {
    ApiName  = aws_api_gateway_rest_api.this.name
    Stage    = aws_api_gateway_stage.this.stage_name
    Resource = each.value.resource
    Method   = each.value.method
  }
  tags = var.tags
}
