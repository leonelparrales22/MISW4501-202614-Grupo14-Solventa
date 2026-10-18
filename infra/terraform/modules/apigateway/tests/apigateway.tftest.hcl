# Configuration tests of the human-channel part of the API Gateway module
# (HA-S01, CA-1 and CA-5). They run with a mocked AWS provider: no
# credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}

variables {
  name             = "solventa-test-ha-s01"
  backend_base_url = "http://internal-alb.example.com"
}

run "partner_only_api_by_default" {
  command = plan

  assert {
    condition     = length(aws_api_gateway_authorizer.cognito) == 0
    error_message = "Without cognito_user_pool_arns there must be no authorizer."
  }

  assert {
    condition     = length(aws_api_gateway_resource.human) == 0 && length(aws_api_gateway_method.human) == 0
    error_message = "Without human_routes there must be no human resources or methods."
  }

  assert {
    condition     = length(aws_api_gateway_gateway_response.cors) == 0
    error_message = "Without the authorizer the gateway responses keep their defaults."
  }

  assert {
    condition = alltrue([
      for m in [aws_api_gateway_method.resolve_post, aws_api_gateway_method.customer_get] :
      m.authorization == "NONE" && m.api_key_required == true
    ])
    error_message = "The partner quoting routes must use only the API key (CA-5)."
  }
}

run "human_routes_use_the_cognito_authorizer" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes = {
      quotes   = { methods = ["GET", "POST"] }
      policies = { methods = ["GET"] }
    }
  }

  assert {
    condition = (
      aws_api_gateway_authorizer.cognito[0].type == "COGNITO_USER_POOLS" &&
      aws_api_gateway_authorizer.cognito[0].identity_source == "method.request.header.Authorization" &&
      aws_api_gateway_authorizer.cognito[0].provider_arns == toset(["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"])
    )
    error_message = "The authorizer must be COGNITO_USER_POOLS on the Authorization header of the given user pool."
  }

  assert {
    condition     = length(aws_api_gateway_method.human) == 6
    error_message = "Each human route needs its methods on the route and on its sub-paths (/{proxy+})."
  }

  assert {
    condition = alltrue([
      for m in aws_api_gateway_method.human :
      m.authorization == "COGNITO_USER_POOLS" && m.authorizer_id == aws_api_gateway_authorizer.cognito[0].id && m.api_key_required == false
    ])
    error_message = "Every human route must go through the Cognito authorizer and never require an API key (CA-1, CA-5)."
  }

  assert {
    condition = alltrue([
      for m in [aws_api_gateway_method.resolve_post, aws_api_gateway_method.customer_get] :
      m.authorization == "NONE" && m.api_key_required == true && m.authorizer_id == null
    ])
    error_message = "Partner routes must keep the API key and never use the Cognito authorizer (CA-5)."
  }

  assert {
    condition = (
      aws_api_gateway_integration.human["quotes GET"].uri == "http://internal-alb.example.com/v1/quotes" &&
      aws_api_gateway_integration.human["quotes/{proxy+} POST"].uri == "http://internal-alb.example.com/v1/quotes/{proxy}" &&
      aws_api_gateway_integration.human["quotes/{proxy+} POST"].request_parameters["integration.request.path.proxy"] == "method.request.path.proxy"
    )
    error_message = "Human routes must be proxied to the same path of the backend."
  }

  assert {
    condition     = alltrue([for i in aws_api_gateway_integration.human : i.type == "HTTP_PROXY" && i.timeout_milliseconds == 5000])
    error_message = "Human routes use HTTP_PROXY with the ADR-02 timeout."
  }

  assert {
    condition = length(aws_api_gateway_method.human_options) == 4 && alltrue([
      for m in aws_api_gateway_method.human_options : m.authorization == "NONE" && m.api_key_required == false
    ])
    error_message = "Every human resource needs an OPTIONS preflight without authorization."
  }

  assert {
    condition     = alltrue([for i in aws_api_gateway_integration.human_options : i.type == "MOCK"])
    error_message = "The CORS preflight is answered by the gateway (MOCK)."
  }

  assert {
    condition = (
      aws_api_gateway_integration_response.human_options["quotes"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'" &&
      aws_api_gateway_integration_response.human_options["quotes"].response_parameters["method.response.header.Access-Control-Allow-Headers"] == "'Authorization,Content-Type'"
    )
    error_message = "The preflight must allow the route methods and the Authorization header."
  }

  assert {
    condition     = toset(keys(aws_api_gateway_gateway_response.cors)) == toset(["UNAUTHORIZED", "ACCESS_DENIED", "EXPIRED_TOKEN", "DEFAULT_4XX"])
    error_message = "The 401/403 and default 4XX gateway responses must be customized."
  }

  assert {
    condition = (
      aws_api_gateway_gateway_response.cors["ACCESS_DENIED"].status_code == "401" &&
      aws_api_gateway_gateway_response.cors["UNAUTHORIZED"].status_code == "401" &&
      aws_api_gateway_gateway_response.cors["EXPIRED_TOKEN"].status_code == "403" &&
      aws_api_gateway_gateway_response.cors["DEFAULT_4XX"].status_code == null
    )
    error_message = "A tampered token of the user pool (ACCESS_DENIED) must answer 401 like any invalid token (HA-S01 CA-1)."
  }

  assert {
    condition = alltrue([
      for r in aws_api_gateway_gateway_response.cors :
      r.response_parameters["gatewayresponse.header.Access-Control-Allow-Origin"] == "'*'"
    ])
    error_message = "Gateway 4XX responses need CORS headers so the browser can read the 401."
  }

  assert {
    condition     = output.cognito_authorizer_id == aws_api_gateway_authorizer.cognito[0].id
    error_message = "The module must expose the authorizer ID."
  }
}

run "cors_origin_can_be_pinned" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { quotes = { methods = ["GET"] } }
    cors_allow_origin      = "https://portal.example.com"
  }

  assert {
    condition = alltrue([
      for r in aws_api_gateway_gateway_response.cors :
      r.response_parameters["gatewayresponse.header.Access-Control-Allow-Origin"] == "'https://portal.example.com'"
    ])
    error_message = "cors_allow_origin must reach the gateway responses."
  }
}

run "human_routes_without_authorizer_are_rejected" {
  command = plan

  variables {
    human_routes = { quotes = { methods = ["GET"] } }
  }

  expect_failures = [aws_api_gateway_resource.human]
}

run "options_is_not_a_route_method" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { quotes = { methods = ["OPTIONS"] } }
  }

  expect_failures = [var.human_routes]
}

run "quoting_journey_cannot_become_human" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { risk-profiles = { methods = ["GET"] } }
  }

  expect_failures = [var.human_routes]
}

run "authorizer_needs_a_user_pool_arn" {
  command = plan

  variables {
    cognito_user_pool_arns = ["us-east-2_Example"]
  }

  expect_failures = [var.cognito_user_pool_arns]
}

# --- Public routes and binary media types (HA-D02) ---------------------------------

run "public_routes_skip_the_authorizer_and_the_api_key" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { policies = { methods = ["GET"] } }
    public_routes          = { documents = { methods = ["GET"], rate_limit = 10, burst_limit = 20 } }
  }

  assert {
    condition = length(aws_api_gateway_method.public) == 2 && alltrue([
      for m in aws_api_gateway_method.public :
      m.authorization == "NONE" && m.api_key_required == false && m.authorizer_id == null && m.http_method == "GET"
    ])
    error_message = "A public route and its sub-paths answer GET without session and without API key."
  }

  assert {
    condition = (
      aws_api_gateway_integration.public["documents GET"].uri == "http://internal-alb.example.com/v1/documents" &&
      aws_api_gateway_integration.public["documents/{proxy+} GET"].uri == "http://internal-alb.example.com/v1/documents/{proxy}" &&
      aws_api_gateway_integration.public["documents/{proxy+} GET"].request_parameters["integration.request.path.proxy"] == "method.request.path.proxy" &&
      alltrue([for i in aws_api_gateway_integration.public : i.type == "HTTP_PROXY" && i.timeout_milliseconds == 5000])
    )
    error_message = "Public routes are proxied to the same path of the backend with the ADR-02 timeout."
  }

  assert {
    condition = length(aws_api_gateway_method_settings.public) == 2 && alltrue([
      for s in aws_api_gateway_method_settings.public :
      s.settings[0].throttling_rate_limit == 10 && s.settings[0].throttling_burst_limit == 20 && s.settings[0].caching_enabled == false
    ])
    error_message = "Each public method is throttled at the stage (decision D-5: 10 req/s, burst 20) and never cached."
  }

  assert {
    condition     = aws_api_gateway_method_settings.public["documents/{proxy+} GET"].method_path == "v1/documents/{proxy+}/GET"
    error_message = "The throttle applies to the method of the signed link."
  }

  assert {
    condition     = output.public_paths == ["/v1/documents", "/v1/documents/{proxy+}"]
    error_message = "The module exposes the public paths."
  }

  assert {
    condition     = length(aws_api_gateway_method.human) == 2 && alltrue([for m in aws_api_gateway_method.human : m.authorization == "COGNITO_USER_POOLS"])
    error_message = "The human routes keep the Cognito authorizer next to the public ones."
  }
}

run "public_routes_are_read_only" {
  command = plan

  variables {
    public_routes = { documents = { methods = ["GET", "POST"] } }
  }

  expect_failures = [var.public_routes]
}

run "a_route_cannot_be_public_and_human" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { documents = { methods = ["GET"] } }
    public_routes          = { documents = { methods = ["GET"] } }
  }

  expect_failures = [aws_api_gateway_resource.public]
}

run "quoting_journey_cannot_become_public" {
  command = plan

  variables {
    public_routes = { risk-profiles = { methods = ["GET"] } }
  }

  expect_failures = [var.public_routes]
}

run "binary_media_types_keep_the_preflight_as_text" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { policies = { methods = ["GET"] } }
    binary_media_types     = ["*/*"]
  }

  assert {
    condition     = aws_api_gateway_rest_api.this.binary_media_types == tolist(["*/*"])
    error_message = "The API passes every media type as binary, so the PDF reaches the client intact (decision D-4)."
  }

  assert {
    condition     = alltrue([for i in aws_api_gateway_integration.human_options : i.content_handling == "CONVERT_TO_TEXT"])
    error_message = "With binary media types the MOCK preflight converts its body to text, so its template still answers 200."
  }

  assert {
    condition     = alltrue([for i in aws_api_gateway_integration.human : i.content_handling == null])
    error_message = "The proxied routes pass the bytes through unchanged."
  }
}

run "defaults_change_nothing" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { policies = { methods = ["GET"] } }
  }

  assert {
    condition = (
      length(aws_api_gateway_resource.public) == 0 && length(aws_api_gateway_method.public) == 0 &&
      length(aws_api_gateway_method_settings.public) == 0 && output.public_paths == []
    )
    error_message = "Without public_routes there are no public resources."
  }

  # binary_media_types is computed by the provider, so the mock fills it; the
  # configuration leaves it null (see main.tf) and the hash below ignores it.
  assert {
    condition     = alltrue([for i in aws_api_gateway_integration.human_options : i.content_handling == null])
    error_message = "Without binary_media_types the preflight stays as before."
  }

  assert {
    condition = aws_api_gateway_deployment.this.triggers.redeployment == sha1(join("", [
      jsonencode([
        aws_api_gateway_resource.resolve.id,
        aws_api_gateway_resource.customer.id,
        aws_api_gateway_method.resolve_post,
        aws_api_gateway_method.customer_get,
        aws_api_gateway_integration.resolve_post,
        aws_api_gateway_integration.customer_get,
      ]),
      jsonencode([
        aws_api_gateway_authorizer.cognito,
        aws_api_gateway_method.human,
        aws_api_gateway_integration.human,
        aws_api_gateway_method.human_options,
        aws_api_gateway_integration.human_options,
        aws_api_gateway_integration_response.human_options,
        aws_api_gateway_gateway_response.cors,
      ]),
    ]))
    error_message = "Without public routes or binary types the redeployment hash is the one of HA-L01 and HA-S01."
  }
}

# --- Public card tokenization (T5) -------------------------------------------------------

run "payment_tokenization_is_public_post_only" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"] } }
    payment_tokenization   = {}
  }

  assert {
    condition = (
      aws_api_gateway_resource.payment_gateway[0].parent_id == aws_api_gateway_rest_api.this.root_resource_id &&
      aws_api_gateway_resource.payment_gateway[0].path_part == "pasarela" &&
      aws_api_gateway_resource.payment_tokens[0].parent_id == aws_api_gateway_resource.payment_gateway[0].id &&
      aws_api_gateway_resource.payment_tokens[0].path_part == "tokens"
    )
    error_message = "The route is /pasarela/tokens, outside /v1 (HU-W1.3-02 contract, section 8)."
  }

  assert {
    condition = (
      length(aws_api_gateway_method.payment_tokens_post) == 1 &&
      aws_api_gateway_method.payment_tokens_post[0].http_method == "POST" &&
      aws_api_gateway_method.payment_tokens_post[0].authorization == "NONE" &&
      aws_api_gateway_method.payment_tokens_post[0].api_key_required == false &&
      aws_api_gateway_method.payment_tokens_post[0].authorizer_id == null
    )
    error_message = "Tokenization is a single POST without session and without API key."
  }

  assert {
    condition = (
      aws_api_gateway_integration.payment_tokens_post[0].type == "HTTP_PROXY" &&
      aws_api_gateway_integration.payment_tokens_post[0].integration_http_method == "POST" &&
      aws_api_gateway_integration.payment_tokens_post[0].uri == "http://internal-alb.example.com/tokens" &&
      aws_api_gateway_integration.payment_tokens_post[0].timeout_milliseconds == 5000
    )
    error_message = "The route is proxied to /tokens of the payment gateway behind the load balancer."
  }

  assert {
    condition = (
      aws_api_gateway_method_settings.payment_tokens[0].method_path == "pasarela/tokens/POST" &&
      aws_api_gateway_method_settings.payment_tokens[0].settings[0].throttling_rate_limit == 5 &&
      aws_api_gateway_method_settings.payment_tokens[0].settings[0].throttling_burst_limit == 10 &&
      aws_api_gateway_method_settings.payment_tokens[0].settings[0].caching_enabled == false
    )
    error_message = "The route is throttled at the stage (5 req/s, burst 10, decision D-4 of T5) and never cached."
  }

  assert {
    condition     = output.payment_tokenization_path == "/pasarela/tokens"
    error_message = "The module exposes the tokenization path."
  }

  assert {
    condition     = alltrue([for m in aws_api_gateway_method.human : m.authorization == "COGNITO_USER_POOLS"])
    error_message = "The human routes keep the Cognito authorizer next to the public tokenization."
  }
}

run "payment_tokenization_defaults_change_nothing" {
  command = plan

  assert {
    condition = (
      length(aws_api_gateway_resource.payment_gateway) == 0 &&
      length(aws_api_gateway_resource.payment_tokens) == 0 &&
      length(aws_api_gateway_method.payment_tokens_post) == 0 &&
      length(aws_api_gateway_integration.payment_tokens_post) == 0 &&
      length(aws_api_gateway_method_settings.payment_tokens) == 0
    )
    error_message = "Without payment_tokenization there is no /pasarela route."
  }

  assert {
    condition     = output.payment_tokenization_path == null
    error_message = "Without payment_tokenization the path output is null."
  }
}

run "payment_tokenization_rejects_relative_backend_path" {
  command = plan

  variables {
    payment_tokenization = { backend_path = "tokens" }
  }

  expect_failures = [var.payment_tokenization]
}
