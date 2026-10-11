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
# - Human channels (HA-S01, optional): with cognito_user_pool_arns, a
#   COGNITO_USER_POOLS authorizer protects the human_routes (portal web and
#   mobile app) and the gateway 4XX responses carry CORS headers. Partner
#   routes never use the authorizer and human routes never require an API key
#   (CA-5). Without cognito_user_pool_arns nothing of this is created.

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

  # The human-channel resources (HA-S01) join the hash only when they exist,
  # so an API without them keeps the same redeployment hash as before.
  triggers = {
    redeployment = sha1(join("", concat(
      [jsonencode([
        aws_api_gateway_resource.resolve.id,
        aws_api_gateway_resource.customer.id,
        aws_api_gateway_method.resolve_post,
        aws_api_gateway_method.customer_get,
        aws_api_gateway_integration.resolve_post,
        aws_api_gateway_integration.customer_get,
      ])],
      local.auth_enabled ? [jsonencode([
        aws_api_gateway_authorizer.cognito,
        aws_api_gateway_method.human,
        aws_api_gateway_integration.human,
        aws_api_gateway_method.human_options,
        aws_api_gateway_integration.human_options,
        aws_api_gateway_integration_response.human_options,
        aws_api_gateway_gateway_response.cors,
      ])] : [],
    )))
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

# --- Human channels: Cognito authorizer, CORS and gateway responses (HA-S01) -------

locals {
  auth_enabled = length(var.cognito_user_pool_arns) > 0

  # /v1/<route> and /v1/<route>/{proxy+} for every human route.
  human_resources = merge(
    { for r in keys(var.human_routes) : r => { route = r, proxy = false } },
    { for r in keys(var.human_routes) : "${r}/{proxy+}" => { route = r, proxy = true } },
  )

  human_resource_ids = merge(
    { for r, res in aws_api_gateway_resource.human : r => res.id },
    { for r, res in aws_api_gateway_resource.human_proxy : "${r}/{proxy+}" => res.id },
  )

  human_methods = merge([
    for key, res in local.human_resources : {
      for m in var.human_routes[res.route].methods : "${key} ${m}" => {
        resource    = key
        route       = res.route
        proxy       = res.proxy
        http_method = m
      }
    }
  ]...)

  cors_allow_headers = "Authorization,Content-Type"

  # 4XX answers produced by the gateway itself (401 of the authorizer, 403,
  # missing API key, throttling...). Without CORS headers the browser hides the
  # status and the web client cannot tell an expired session from a CORS error.
  cors_gateway_responses = local.auth_enabled ? toset(["UNAUTHORIZED", "ACCESS_DENIED", "EXPIRED_TOKEN", "DEFAULT_4XX"]) : toset([])

  # Status code of each customized gateway response. UNAUTHORIZED and
  # EXPIRED_TOKEN keep the API Gateway defaults (written explicitly because AWS
  # stores them and a null would show a perpetual diff). With only the Cognito
  # authorizer, ACCESS_DENIED means a token of this user pool whose signature
  # or payload was tampered with: AWS answers 403 by default (verified in the
  # HA-S01 staging window), but an invalid token must get 401 so the client
  # signs in again. A missing API key is INVALID_API_KEY and keeps its 403.
  # Revisit this if a resource policy or another authorizer is added.
  gateway_response_status = {
    UNAUTHORIZED  = "401"
    ACCESS_DENIED = "401"
    EXPIRED_TOKEN = "403"
  }
}

resource "aws_api_gateway_authorizer" "cognito" {
  count = local.auth_enabled ? 1 : 0

  name            = "${var.name}-cognito"
  rest_api_id     = aws_api_gateway_rest_api.this.id
  type            = "COGNITO_USER_POOLS"
  provider_arns   = var.cognito_user_pool_arns
  identity_source = "method.request.header.Authorization"
}

resource "aws_api_gateway_resource" "human" {
  for_each = var.human_routes

  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.v1.id
  path_part   = each.key

  lifecycle {
    precondition {
      condition     = local.auth_enabled
      error_message = "human_routes need cognito_user_pool_arns: a human route is never published without the Cognito authorizer (HA-S01 CA-1)."
    }
  }
}

resource "aws_api_gateway_resource" "human_proxy" {
  for_each = var.human_routes

  rest_api_id = aws_api_gateway_rest_api.this.id
  parent_id   = aws_api_gateway_resource.human[each.key].id
  path_part   = "{proxy+}"
}

# Every human method goes through the authorizer and never asks for an API key.
resource "aws_api_gateway_method" "human" {
  for_each = local.human_methods

  rest_api_id      = aws_api_gateway_rest_api.this.id
  resource_id      = local.human_resource_ids[each.value.resource]
  http_method      = each.value.http_method
  authorization    = "COGNITO_USER_POOLS"
  authorizer_id    = one(aws_api_gateway_authorizer.cognito[*].id)
  api_key_required = false

  request_parameters = each.value.proxy ? { "method.request.path.proxy" = true } : null
}

# The Authorization header reaches the service, which verifies the JWT again
# with pkg/security (decision D-1) and authorizes by role and ownership.
resource "aws_api_gateway_integration" "human" {
  for_each = local.human_methods

  rest_api_id             = aws_api_gateway_rest_api.this.id
  resource_id             = local.human_resource_ids[each.value.resource]
  http_method             = aws_api_gateway_method.human[each.key].http_method
  type                    = "HTTP_PROXY"
  integration_http_method = each.value.http_method
  uri                     = each.value.proxy ? "${local.backend}/v1/${each.value.route}/{proxy}" : "${local.backend}/v1/${each.value.route}"
  connection_type         = local.connection_type
  connection_id           = var.vpc_link_v2_id
  integration_target      = local.private_integration ? var.integration_target_arn : null
  timeout_milliseconds    = var.integration_timeout_ms

  request_parameters = each.value.proxy ? { "integration.request.path.proxy" = "method.request.path.proxy" } : null
}

# CORS preflight: OPTIONS never asks for a token or an API key.
resource "aws_api_gateway_method" "human_options" {
  for_each = local.human_resources

  rest_api_id      = aws_api_gateway_rest_api.this.id
  resource_id      = local.human_resource_ids[each.key]
  http_method      = "OPTIONS"
  authorization    = "NONE"
  api_key_required = false
}

resource "aws_api_gateway_integration" "human_options" {
  for_each = local.human_resources

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = local.human_resource_ids[each.key]
  http_method = aws_api_gateway_method.human_options[each.key].http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = "{\"statusCode\": 200}"
  }
}

resource "aws_api_gateway_method_response" "human_options" {
  for_each = local.human_resources

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = local.human_resource_ids[each.key]
  http_method = aws_api_gateway_method.human_options[each.key].http_method
  status_code = "200"

  response_parameters = {
    "method.response.header.Access-Control-Allow-Origin"  = true
    "method.response.header.Access-Control-Allow-Methods" = true
    "method.response.header.Access-Control-Allow-Headers" = true
  }
}

resource "aws_api_gateway_integration_response" "human_options" {
  for_each = local.human_resources

  rest_api_id = aws_api_gateway_rest_api.this.id
  resource_id = local.human_resource_ids[each.key]
  http_method = aws_api_gateway_method.human_options[each.key].http_method
  status_code = aws_api_gateway_method_response.human_options[each.key].status_code

  response_parameters = {
    "method.response.header.Access-Control-Allow-Origin"  = "'${var.cors_allow_origin}'"
    "method.response.header.Access-Control-Allow-Methods" = "'${join(",", concat(var.human_routes[each.value.route].methods, ["OPTIONS"]))}'"
    "method.response.header.Access-Control-Allow-Headers" = "'${local.cors_allow_headers}'"
  }

  depends_on = [aws_api_gateway_integration.human_options]
}

# Same body as the API Gateway default; the CORS headers are added and the
# status of ACCESS_DENIED becomes 401 (see local.gateway_response_status).
resource "aws_api_gateway_gateway_response" "cors" {
  for_each = local.cors_gateway_responses

  rest_api_id   = aws_api_gateway_rest_api.this.id
  response_type = each.key
  status_code   = lookup(local.gateway_response_status, each.key, null)

  response_parameters = {
    "gatewayresponse.header.Access-Control-Allow-Origin"  = "'${var.cors_allow_origin}'"
    "gatewayresponse.header.Access-Control-Allow-Headers" = "'${local.cors_allow_headers}'"
  }

  response_templates = {
    "application/json" = "{\"message\":$context.error.messageString}"
  }
}
