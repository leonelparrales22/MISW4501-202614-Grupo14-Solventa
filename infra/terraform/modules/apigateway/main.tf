# Regional REST API in front of the origination service (HA-L01).
#
# - POST /v1/risk-profiles/resolve: quoting journey, never cached (the cache
#   key cannot include a body).
# - GET /v1/risk-profiles/{customer_id}?consent_active=: same answer, cached by
#   the stage (CA-4). The cache key is the customer, the consent flag and the
#   partner API key, so partners never share entries.
# - Every route requires an API key; each partner has its own usage plan with
#   rate limiting (CA-5).
# - Integration: HTTP_PROXY, 5 s timeout (ADR-02), through a VPC link V2 to the
#   internal ALB when vpc_link_v2_id is set.

locals {
  private_integration = var.vpc_link_v2_id != null
  connection_type     = local.private_integration ? "VPC_LINK" : "INTERNET"
  backend             = trimsuffix(var.backend_base_url, "/")
  cached_method_path  = "v1/risk-profiles/{customer_id}/GET"
}

resource "aws_api_gateway_rest_api" "this" {
  name        = var.name
  description = "Solventa origination - quoting journey (HA-L01)"
  tags        = var.tags

  endpoint_configuration {
    types = ["REGIONAL"]
  }
}

resource "aws_api_gateway_resource" "v1" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_rest_api.this.root_resource_id
  path_part   = "v1"
}

resource "aws_api_gateway_resource" "risk_profiles" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.v1.id
  path_part   = "risk-profiles"
}

resource "aws_api_gateway_resource" "resolve" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.risk_profiles.id
  path_part   = "resolve"
}

resource "aws_api_gateway_resource" "customer" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.risk_profiles.id
  path_part   = "{customer_id}"
}

# --- POST /v1/risk-profiles/resolve ---------------------------------------------

resource "aws_api_gateway_method" "resolve_post" {
  rest_api_id      = aws_api_gateway_rest_api.this.id
  resource_id      = aws_api_gateway_resource.resolve.id
  http_method      = "POST"
  authorization    = "NONE"
  api_key_required = true
}

resource "aws_api_gateway_integration" "resolve_post" {
  rest_api_id             = aws_api_gateway_rest_api.this.id
  resource_id             = aws_api_gateway_resource.resolve.id
  http_method             = aws_api_gateway_method.resolve_post.http_method
  type                    = "HTTP_PROXY"
  integration_http_method = "POST"
  uri                     = "${local.backend}/v1/risk-profiles/resolve"
  connection_type         = local.connection_type
  connection_id           = var.vpc_link_v2_id
  integration_target      = local.private_integration ? var.integration_target_arn : null
  timeout_milliseconds    = var.integration_timeout_ms
}

# --- GET /v1/risk-profiles/{customer_id} (cached per partner) -------------------

resource "aws_api_gateway_method" "customer_get" {
  rest_api_id      = aws_api_gateway_rest_api.this.id
  resource_id      = aws_api_gateway_resource.customer.id
  http_method      = "GET"
  authorization    = "NONE"
  api_key_required = true

  # Every cache key parameter must be declared in the method request.
  request_parameters = {
    "method.request.path.customer_id"           = true
    "method.request.querystring.consent_active" = true
    "method.request.header.x-api-key"           = true
  }
}

resource "aws_api_gateway_integration" "customer_get" {
  rest_api_id             = aws_api_gateway_rest_api.this.id
  resource_id             = aws_api_gateway_resource.customer.id
  http_method             = aws_api_gateway_method.customer_get.http_method
  type                    = "HTTP_PROXY"
  integration_http_method = "GET"
  uri                     = "${local.backend}/v1/risk-profiles/{customer_id}"
  connection_type         = local.connection_type
  connection_id           = var.vpc_link_v2_id
  integration_target      = local.private_integration ? var.integration_target_arn : null
  timeout_milliseconds    = var.integration_timeout_ms

  request_parameters = {
    "integration.request.path.customer_id"           = "method.request.path.customer_id"
    "integration.request.querystring.consent_active" = "method.request.querystring.consent_active"
  }

  # Partner isolation (CA-4): the partner API key is part of the cache key.
  cache_key_parameters = [
    "method.request.path.customer_id",
    "method.request.querystring.consent_active",
    "method.request.header.x-api-key",
  ]
}

# --- Deployment and stage ----------------------------------------------------------

resource "aws_api_gateway_deployment" "this" {
  rest_api_id = aws_api_gateway_rest_api.this.id

  triggers = {
    redeployment = sha1(jsonencode([
      aws_api_gateway_resource.resolve.id,
      aws_api_gateway_resource.customer.id,
      aws_api_gateway_method.resolve_post,
      aws_api_gateway_method.customer_get,
      aws_api_gateway_integration.resolve_post,
      aws_api_gateway_integration.customer_get,
    ]))
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_api_gateway_stage" "this" {
  rest_api_id           = aws_api_gateway_rest_api.this.id
  deployment_id         = aws_api_gateway_deployment.this.id
  stage_name            = var.stage_name
  cache_cluster_enabled = true
  cache_cluster_size    = var.cache_cluster_size
  tags                  = var.tags
}

# Caching is off for every method except the cacheable GET.
resource "aws_api_gateway_method_settings" "all" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  stage_name  = aws_api_gateway_stage.this.stage_name
  method_path = "*/*"

  settings {
    metrics_enabled = true
    caching_enabled = false
  }
}

resource "aws_api_gateway_method_settings" "cached_get" {
  rest_api_id = aws_api_gateway_rest_api.this.id
  stage_name  = aws_api_gateway_stage.this.stage_name
  method_path = local.cached_method_path

  settings {
    metrics_enabled                            = true
    caching_enabled                            = true
    cache_ttl_in_seconds                       = var.cache_ttl_seconds
    cache_data_encrypted                       = true
    require_authorization_for_cache_control    = true
    unauthorized_cache_control_header_strategy = "SUCCEED_WITH_RESPONSE_HEADER"
  }

  depends_on = [aws_api_gateway_method_settings.all]
}

# --- Partners: API keys and usage plans (rate limiting) -----------------------------

resource "aws_api_gateway_api_key" "partner" {
  for_each = var.partners

  name    = "${var.name}-${each.key}"
  enabled = true
  tags    = var.tags
}

resource "aws_api_gateway_usage_plan" "partner" {
  for_each = var.partners

  name        = "${var.name}-${each.key}"
  description = "Rate limiting of ${each.key} (HA-L01 CA-5)"
  tags        = var.tags

  api_stages {
    api_id = aws_api_gateway_rest_api.this.id
    stage  = aws_api_gateway_stage.this.stage_name
  }

  throttle_settings {
    rate_limit  = each.value.rate_limit
    burst_limit = each.value.burst_limit
  }
}

resource "aws_api_gateway_usage_plan_key" "partner" {
  for_each = var.partners

  key_id        = aws_api_gateway_api_key.partner[each.key].id
  key_type      = "API_KEY"
  usage_plan_id = aws_api_gateway_usage_plan.partner[each.key].id
}
