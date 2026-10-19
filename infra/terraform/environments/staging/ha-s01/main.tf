# Ephemeral staging window to verify HA-S01 in AWS (CA-1 to CA-5) with
# tests/security/ha-s01/verify-access.sh and the optional pkg/security
# integration test (cognito_staging_integration_test.go):
#
#   curl --> Cognito user pool (InitiateAuth, SignUp, UpdateUserAttributes, RevokeToken)
#   curl --> test REST API (MOCK integrations, no backend, no stage cache):
#              GET /ha-s01/humana     Cognito authorizer; echoes the claims it received
#              GET /ha-s01/socio      API key + usage plan only
#              GET /v1/ha-s01-check   Cognito authorizer; echoes the claims (backend of the module)
#   curl --> real modules/apigateway (stage cache 0.5 GB, detailed metrics):
#              GET /v1/ha-s01-check   human route: Cognito authorizer -> HTTP_PROXY to the
#                                     test API above; a 200 with the claims proves that the
#                                     module forwards the Authorization header
#              OPTIONS                CORS preflight without authorization; 4XX with CORS
#              GET /v1/risk-profiles/{customer_id}  partner route, API key only
#
# No VPC, NAT, ALB, Fargate, RDS, Redis or CloudWatch logs. Everything is
# destroyed in the same session (R8/R12 of Nicolás's workflow).

module "cognito" {
  source = "../../../modules/cognito"

  name                = var.name
  deletion_protection = false # ephemeral window: destroy must remove the pool
  seed_users          = var.seed_users
  seed_user_password  = var.seed_user_password
}

# --- Test REST API -------------------------------------------------------------------

resource "aws_api_gateway_rest_api" "test" {
  name        = var.name
  description = "Solventa HA-S01 access verification (MOCK integrations, staging window only)"

  endpoint_configuration {
    types = ["REGIONAL"]
  }
}

resource "aws_api_gateway_authorizer" "cognito" {
  name            = "${var.name}-cognito"
  rest_api_id     = aws_api_gateway_rest_api.test.id
  type            = "COGNITO_USER_POOLS"
  provider_arns   = [module.cognito.user_pool_arn]
  identity_source = "method.request.header.Authorization"
}

resource "aws_api_gateway_resource" "story" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  parent_id   = aws_api_gateway_rest_api.test.root_resource_id
  path_part   = "ha-s01"
}

locals {
  routes = {
    humana = { authorization = "COGNITO_USER_POOLS", api_key_required = false }
    socio  = { authorization = "NONE", api_key_required = true }
  }

  # The human route echoes the claims that reach the integration (they travel
  # inside the routed request); the partner route answers a fixed body. The
  # backend of the real module echoes the claims too, under its own route name.
  response_templates = {
    humana         = <<-EOT
      {"route":"humana","sub":"$context.authorizer.claims.sub","groups":"$context.authorizer.claims['cognito:groups']","partner_id":"$context.authorizer.claims['custom:partner_id']","token_use":"$context.authorizer.claims.token_use"}
    EOT
    socio          = <<-EOT
      {"route":"socio","api_key_id":"$context.identity.apiKeyId"}
    EOT
    module_backend = <<-EOT
      {"route":"module-backend","sub":"$context.authorizer.claims.sub","groups":"$context.authorizer.claims['cognito:groups']","partner_id":"$context.authorizer.claims['custom:partner_id']","token_use":"$context.authorizer.claims.token_use"}
    EOT
  }

  # Human route of the real apigateway module (path segment under /v1).
  module_check_route = "ha-s01-check"

  cors_headers = {
    "method.response.header.Access-Control-Allow-Origin"  = "'*'"
    "method.response.header.Access-Control-Allow-Methods" = "'GET,OPTIONS'"
    "method.response.header.Access-Control-Allow-Headers" = "'Authorization,Content-Type,x-api-key'"
  }
}

resource "aws_api_gateway_resource" "route" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  parent_id   = aws_api_gateway_resource.story.id
  path_part   = each.key
}

resource "aws_api_gateway_method" "get" {
  for_each = local.routes

  rest_api_id      = aws_api_gateway_rest_api.test.id
  resource_id      = aws_api_gateway_resource.route[each.key].id
  http_method      = "GET"
  authorization    = each.value.authorization
  authorizer_id    = each.value.authorization == "COGNITO_USER_POOLS" ? aws_api_gateway_authorizer.cognito.id : null
  api_key_required = each.value.api_key_required
}

resource "aws_api_gateway_integration" "get" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.get[each.key].http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = "{\"statusCode\": 200}"
  }
}

resource "aws_api_gateway_method_response" "get" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.get[each.key].http_method
  status_code = "200"

  response_parameters = { "method.response.header.Access-Control-Allow-Origin" = true }
}

resource "aws_api_gateway_integration_response" "get" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.get[each.key].http_method
  status_code = aws_api_gateway_method_response.get[each.key].status_code

  response_parameters = { "method.response.header.Access-Control-Allow-Origin" = "'*'" }
  response_templates  = { "application/json" = trimspace(local.response_templates[each.key]) }

  depends_on = [aws_api_gateway_integration.get]
}

# CORS preflight on both routes, without authorization or API key.
resource "aws_api_gateway_method" "options" {
  for_each = local.routes

  rest_api_id      = aws_api_gateway_rest_api.test.id
  resource_id      = aws_api_gateway_resource.route[each.key].id
  http_method      = "OPTIONS"
  authorization    = "NONE"
  api_key_required = false
}

resource "aws_api_gateway_integration" "options" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.options[each.key].http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = "{\"statusCode\": 200}"
  }
}

resource "aws_api_gateway_method_response" "options" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.options[each.key].http_method
  status_code = "200"

  response_parameters = { for header, _ in local.cors_headers : header => true }
}

resource "aws_api_gateway_integration_response" "options" {
  for_each = local.routes

  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.route[each.key].id
  http_method = aws_api_gateway_method.options[each.key].http_method
  status_code = aws_api_gateway_method_response.options[each.key].status_code

  response_parameters = local.cors_headers

  depends_on = [aws_api_gateway_integration.options]
}

# --- Backend of the real apigateway module: GET /v1/ha-s01-check (BN-2) -------------
# The module proxies its human route (HTTP_PROXY) to this route, which runs the
# same Cognito authorizer and echoes the claims. If the module did not forward
# the Authorization header, this authorizer would answer 401 instead of 200.

resource "aws_api_gateway_resource" "v1" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  parent_id   = aws_api_gateway_rest_api.test.root_resource_id
  path_part   = "v1"
}

resource "aws_api_gateway_resource" "module_backend" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  parent_id   = aws_api_gateway_resource.v1.id
  path_part   = local.module_check_route
}

resource "aws_api_gateway_method" "module_backend" {
  rest_api_id      = aws_api_gateway_rest_api.test.id
  resource_id      = aws_api_gateway_resource.module_backend.id
  http_method      = "GET"
  authorization    = "COGNITO_USER_POOLS"
  authorizer_id    = aws_api_gateway_authorizer.cognito.id
  api_key_required = false
}

resource "aws_api_gateway_integration" "module_backend" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.module_backend.id
  http_method = aws_api_gateway_method.module_backend.http_method
  type        = "MOCK"

  request_templates = {
    "application/json" = "{\"statusCode\": 200}"
  }
}

resource "aws_api_gateway_method_response" "module_backend" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.module_backend.id
  http_method = aws_api_gateway_method.module_backend.http_method
  status_code = "200"

  response_parameters = { "method.response.header.Access-Control-Allow-Origin" = true }
}

resource "aws_api_gateway_integration_response" "module_backend" {
  rest_api_id = aws_api_gateway_rest_api.test.id
  resource_id = aws_api_gateway_resource.module_backend.id
  http_method = aws_api_gateway_method.module_backend.http_method
  status_code = aws_api_gateway_method_response.module_backend.status_code

  response_parameters = { "method.response.header.Access-Control-Allow-Origin" = "'*'" }
  response_templates  = { "application/json" = trimspace(local.response_templates.module_backend) }

  depends_on = [aws_api_gateway_integration.module_backend]
}

# Same mapping as modules/apigateway: a tampered token of the pool
# (ACCESS_DENIED, 403 by default) answers 401 like any invalid token (CA-1).
resource "aws_api_gateway_gateway_response" "access_denied" {
  rest_api_id   = aws_api_gateway_rest_api.test.id
  response_type = "ACCESS_DENIED"
  status_code   = "401"

  response_parameters = {
    "gatewayresponse.header.Access-Control-Allow-Origin" = "'*'"
  }

  response_templates = {
    "application/json" = "{\"message\":$context.error.messageString}"
  }
}

resource "aws_api_gateway_deployment" "test" {
  rest_api_id = aws_api_gateway_rest_api.test.id

  triggers = {
    redeployment = sha1(jsonencode([
      aws_api_gateway_gateway_response.access_denied,
      aws_api_gateway_authorizer.cognito,
      aws_api_gateway_method.get,
      aws_api_gateway_integration.get,
      aws_api_gateway_integration_response.get,
      aws_api_gateway_method.options,
      aws_api_gateway_integration.options,
      aws_api_gateway_integration_response.options,
      aws_api_gateway_method.module_backend,
      aws_api_gateway_integration.module_backend,
      aws_api_gateway_integration_response.module_backend,
    ]))
  }

  lifecycle {
    create_before_destroy = true
  }
}

# No cache cluster, no access or execution logs (cost of this API ~ US$ 0).
resource "aws_api_gateway_stage" "test" {
  rest_api_id   = aws_api_gateway_rest_api.test.id
  deployment_id = aws_api_gateway_deployment.test.id
  stage_name    = "staging"
}

# --- Partner machine-to-machine access: API key + usage plan (CA-5) ----------------

resource "aws_api_gateway_api_key" "partner" {
  name    = "${var.name}-socio-a"
  enabled = true
}

resource "aws_api_gateway_usage_plan" "partner" {
  name        = "${var.name}-socio-a"
  description = "Test partner of the HA-S01 window (CA-5)"

  api_stages {
    api_id = aws_api_gateway_rest_api.test.id
    stage  = aws_api_gateway_stage.test.stage_name
  }

  throttle_settings {
    rate_limit  = 5
    burst_limit = 10
  }
}

resource "aws_api_gateway_usage_plan_key" "partner" {
  key_id        = aws_api_gateway_api_key.partner.id
  key_type      = "API_KEY"
  usage_plan_id = aws_api_gateway_usage_plan.partner.id
}

# --- Real modules/apigateway with the Cognito authorizer (BN-2) ----------------------
# The artifact the team will use, unchanged: authorizer on human_routes (and
# their {proxy+}), OPTIONS without authorization, gateway 4XX answers with CORS
# and the partner routes behind the API key. Its "backend" is the MOCK route
# /v1/ha-s01-check of the test API (INTERNET integration, no VPC link).
# It creates the 0.5 GB stage cache and detailed metrics: ~ US$ 0.036/h.

module "apigateway" {
  source = "../../../modules/apigateway"

  name                   = "${var.name}-module"
  backend_base_url       = aws_api_gateway_stage.test.invoke_url
  cognito_user_pool_arns = [module.cognito.user_pool_arn]
  human_routes           = { (local.module_check_route) = { methods = ["GET"] } }
  partners               = { socio-a = { rate_limit = 5, burst_limit = 10 } }
  # cors_allow_origin keeps its default "*" (the web client sends a Bearer token).
}
