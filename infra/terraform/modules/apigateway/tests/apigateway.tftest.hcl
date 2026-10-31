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

run "registration_is_public_post_only" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { policies = { methods = ["GET"] } }
    registration_route     = {}
  }

  assert {
    condition = (
      aws_api_gateway_resource.registrations[0].parent_id == aws_api_gateway_resource.v1.id &&
      aws_api_gateway_resource.registrations[0].path_part == "registrations"
    )
    error_message = "The sign-up route is /v1/registrations."
  }

  assert {
    condition = (
      length(aws_api_gateway_method.registrations_post) == 1 &&
      aws_api_gateway_method.registrations_post[0].http_method == "POST" &&
      aws_api_gateway_method.registrations_post[0].authorization == "NONE" &&
      aws_api_gateway_method.registrations_post[0].api_key_required == false &&
      aws_api_gateway_method.registrations_post[0].authorizer_id == null
    )
    error_message = "Sign-up is a single POST without session and without API key: the account does not exist yet."
  }

  assert {
    condition = (
      aws_api_gateway_integration.registrations_post[0].type == "HTTP_PROXY" &&
      aws_api_gateway_integration.registrations_post[0].integration_http_method == "POST" &&
      aws_api_gateway_integration.registrations_post[0].uri == "http://internal-alb.example.com/v1/registrations" &&
      aws_api_gateway_integration.registrations_post[0].timeout_milliseconds == 15000
    )
    error_message = "The route is proxied to /v1/registrations of origination behind the load balancer, with 15 s for the identity check."
  }

  assert {
    condition = (
      aws_api_gateway_resource.registration_document_reads[0].parent_id == aws_api_gateway_resource.registrations[0].id &&
      aws_api_gateway_resource.registration_document_reads[0].path_part == "document-reads" &&
      aws_api_gateway_method.registration_document_reads_post[0].http_method == "POST" &&
      aws_api_gateway_method.registration_document_reads_post[0].authorization == "NONE" &&
      aws_api_gateway_method.registration_document_reads_post[0].api_key_required == false &&
      aws_api_gateway_integration.registration_document_reads_post[0].uri == "http://internal-alb.example.com/v1/registrations/document-reads" &&
      aws_api_gateway_integration.registration_document_reads_post[0].timeout_milliseconds == 15000 &&
      aws_api_gateway_method_settings.registration_document_reads[0].method_path == "v1/registrations/document-reads/POST" &&
      aws_api_gateway_method_settings.registration_document_reads[0].settings[0].throttling_rate_limit == 2
    )
    error_message = "The read of the document (HU-M1.1-01, criterion 1) is a public POST under /v1/registrations, proxied to origination and throttled like the sign-up."
  }

  assert {
    condition = (
      aws_api_gateway_method_settings.registrations[0].method_path == "v1/registrations/POST" &&
      aws_api_gateway_method_settings.registrations[0].settings[0].throttling_rate_limit == 2 &&
      aws_api_gateway_method_settings.registrations[0].settings[0].throttling_burst_limit == 5 &&
      aws_api_gateway_method_settings.registrations[0].settings[0].caching_enabled == false
    )
    error_message = "The route is throttled at the stage (2 req/s, burst 5) and never cached."
  }

  assert {
    condition     = output.registration_path == "/v1/registrations"
    error_message = "The module exposes the sign-up path."
  }

  assert {
    condition     = alltrue([for m in aws_api_gateway_method.human : m.authorization == "COGNITO_USER_POOLS"])
    error_message = "The human routes keep the Cognito authorizer next to the public sign-up."
  }
}

run "registration_defaults_change_nothing" {
  command = plan

  assert {
    condition = (
      length(aws_api_gateway_resource.registrations) == 0 &&
      length(aws_api_gateway_method.registrations_post) == 0 &&
      length(aws_api_gateway_integration.registrations_post) == 0 &&
      length(aws_api_gateway_method_settings.registrations) == 0 &&
      output.registration_path == null
    )
    error_message = "Without registration_route nothing of the sign-up route is created."
  }
}

run "registration_rejects_relative_backend_path" {
  command = plan

  variables {
    registration_route = { backend_path = "v1/registrations" }
  }

  expect_failures = [var.registration_route]
}

run "registration_rejects_a_throttle_of_zero" {
  command = plan

  variables {
    registration_route = { rate_limit = 0 }
  }

  expect_failures = [var.registration_route]
}

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

# --- Partner routes on explicit paths and root_methods (T5, decision D-22) --------------

run "partner_routes_default_creates_nothing" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"] } }
  }

  assert {
    condition = (
      length(aws_api_gateway_resource.partner_l1) == 0 && length(aws_api_gateway_resource.partner_l2) == 0 &&
      length(aws_api_gateway_resource.partner_l3) == 0 && length(aws_api_gateway_method.partner) == 0 &&
      length(aws_api_gateway_integration.partner) == 0 && output.partner_paths == []
    )
    error_message = "Without partner_routes there are no partner resources or methods."
  }
}

run "partner_routes_require_api_key_and_no_authorizer" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { policies = { methods = ["GET"] } }
    partner_routes = {
      "audit/consent-terms/current" = { methods = ["GET"] }
      "audit/consents"              = { methods = ["POST"] }
    }
  }

  assert {
    condition = length(aws_api_gateway_method.partner) == 2 && alltrue([
      for m in aws_api_gateway_method.partner :
      m.authorization == "NONE" && m.api_key_required == true && m.authorizer_id == null
    ])
    error_message = "A partner route asks for the partner API key and never uses the Cognito authorizer (CA-5)."
  }

  assert {
    condition = (
      aws_api_gateway_integration.partner["audit/consent-terms/current GET"].uri == "http://internal-alb.example.com/v1/audit/consent-terms/current" &&
      aws_api_gateway_integration.partner["audit/consents POST"].uri == "http://internal-alb.example.com/v1/audit/consents" &&
      alltrue([for i in aws_api_gateway_integration.partner : i.type == "HTTP_PROXY" && i.timeout_milliseconds == 5000])
    )
    error_message = "Partner routes are proxied to the same exact path of the backend with the ADR-02 timeout."
  }

  assert {
    condition     = length(aws_api_gateway_method.human_options) == 2
    error_message = "Partner routes get no preflight: only the human resources have OPTIONS."
  }

  assert {
    condition     = output.partner_paths == ["/v1/audit/consent-terms/current", "/v1/audit/consents"]
    error_message = "The module exposes the partner paths."
  }
}

run "nested_partner_routes_build_their_parents" {
  command = apply

  variables {
    partner_routes = {
      "audit/consent-terms/current" = { methods = ["GET"] }
      "audit/consents"              = { methods = ["POST"] }
    }
  }

  assert {
    condition = (
      keys(aws_api_gateway_resource.partner_l1) == ["audit"] &&
      toset(keys(aws_api_gateway_resource.partner_l2)) == toset(["audit/consent-terms", "audit/consents"]) &&
      keys(aws_api_gateway_resource.partner_l3) == ["audit/consent-terms/current"]
    )
    error_message = "Each prefix of the keys gets one resource, by depth."
  }

  assert {
    condition = (
      aws_api_gateway_resource.partner_l1["audit"].parent_id == aws_api_gateway_resource.v1.id &&
      aws_api_gateway_resource.partner_l2["audit/consents"].parent_id == aws_api_gateway_resource.partner_l1["audit"].id &&
      aws_api_gateway_resource.partner_l2["audit/consent-terms"].path_part == "consent-terms" &&
      aws_api_gateway_resource.partner_l3["audit/consent-terms/current"].parent_id == aws_api_gateway_resource.partner_l2["audit/consent-terms"].id &&
      aws_api_gateway_resource.partner_l3["audit/consent-terms/current"].path_part == "current"
    )
    error_message = "Each partner resource hangs from its parent: /v1/audit/consent-terms/current."
  }

  assert {
    condition = (
      aws_api_gateway_method.partner["audit/consents POST"].resource_id == aws_api_gateway_resource.partner_l2["audit/consents"].id &&
      aws_api_gateway_method.partner["audit/consent-terms/current GET"].resource_id == aws_api_gateway_resource.partner_l3["audit/consent-terms/current"].id
    )
    error_message = "Each method is on the resource of its exact path; the intermediate audit/consent-terms has none."
  }
}

run "partner_route_shares_the_root_of_a_human_route" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"], root_methods = [] } }
    partner_routes         = { offers = { methods = ["POST"] } }
  }

  assert {
    condition = (
      length(aws_api_gateway_resource.partner_l1) == 0 &&
      aws_api_gateway_method.partner["offers POST"].resource_id == aws_api_gateway_resource.human["offers"].id &&
      aws_api_gateway_method.partner["offers POST"].api_key_required == true &&
      aws_api_gateway_method.partner["offers POST"].authorization == "NONE"
    )
    error_message = "POST /v1/offers takes the root of the human route offers, with the partner API key."
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human)) == toset(["offers/{proxy+} GET", "offers/{proxy+} POST"]) &&
      alltrue([for m in aws_api_gateway_method.human : m.authorization == "COGNITO_USER_POOLS" && m.api_key_required == false])
    )
    error_message = "/v1/offers/{proxy+} keeps GET and POST with Cognito; the root has no human method."
  }

  assert {
    condition = (
      keys(aws_api_gateway_method.human_options) == ["offers/{proxy+}"] &&
      aws_api_gateway_integration_response.human_options["offers/{proxy+}"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'"
    )
    error_message = "The root without human methods has no preflight; the sub-paths keep theirs."
  }
}

run "human_root_methods_default_to_methods" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes = {
      quotes  = { methods = ["GET", "POST"] }
      reviews = { methods = ["GET", "POST"], root_methods = ["GET"] }
    }
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human)) == toset([
        "quotes GET", "quotes POST", "quotes/{proxy+} GET", "quotes/{proxy+} POST",
        "reviews GET", "reviews/{proxy+} GET", "reviews/{proxy+} POST",
      ])
    )
    error_message = "Without root_methods the root has the same methods as its sub-paths; with them, only those."
  }

  assert {
    condition = (
      aws_api_gateway_integration_response.human_options["quotes"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'" &&
      aws_api_gateway_integration_response.human_options["reviews"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,OPTIONS'" &&
      aws_api_gateway_integration_response.human_options["reviews/{proxy+}"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'"
    )
    error_message = "Each preflight allows the methods of its own resource."
  }
}

run "partner_routes_reject_the_quoting_journey" {
  command = plan

  variables {
    partner_routes = { "risk-profiles/x" = { methods = ["POST"] } }
  }

  expect_failures = [var.partner_routes]
}

run "partner_routes_reject_a_public_route" {
  command = plan

  variables {
    public_routes  = { documents = { methods = ["GET"] } }
    partner_routes = { "documents/sign" = { methods = ["POST"] } }
  }

  expect_failures = [aws_api_gateway_resource.partner_l1]
}

run "partner_routes_reject_a_deep_path_under_a_human_route" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"] } }
    partner_routes         = { "offers/batch" = { methods = ["POST"] } }
  }

  expect_failures = [aws_api_gateway_method.partner]
}

run "partner_routes_reject_a_method_of_the_human_root" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"] } }
    partner_routes         = { offers = { methods = ["POST"] } }
  }

  expect_failures = [aws_api_gateway_method.partner]
}

# --- Human sub-paths under a partner route (HU-W4.2-02) ---------------------------------

run "human_subroutes_default_creates_nothing" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
  }

  assert {
    condition     = length(aws_api_gateway_resource.human_subproxy) == 0 && length(aws_api_gateway_method.human) == 0
    error_message = "Without human_subroutes there is no extra resource and no human method."
  }
}

run "human_subroutes_publish_only_the_proxy_under_a_partner_route" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes = {
      "audit/consent-terms/current" = { methods = ["GET"] }
      "audit/consents"              = { methods = ["POST"] }
    }
    human_subroutes = { "audit/consents" = { methods = ["GET", "POST"] } }
  }

  assert {
    condition = (
      keys(aws_api_gateway_resource.human_subproxy) == ["audit/consents"] &&
      aws_api_gateway_resource.human_subproxy["audit/consents"].path_part == "{proxy+}" &&
      aws_api_gateway_resource.human_subproxy["audit/consents"].parent_id == aws_api_gateway_resource.partner_l2["audit/consents"].id
    )
    error_message = "Only the {proxy+} under /v1/audit/consents is created."
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human)) == toset(["audit/consents/{proxy+} GET", "audit/consents/{proxy+} POST"]) &&
      alltrue([
        for m in aws_api_gateway_method.human :
        m.authorization == "COGNITO_USER_POOLS" && m.api_key_required == false && m.authorizer_id != null
      ])
    )
    error_message = "The sub-route uses the Cognito authorizer and never asks for an API key."
  }

  assert {
    condition = (
      aws_api_gateway_integration.human["audit/consents/{proxy+} GET"].uri == "http://internal-alb.example.com/v1/audit/consents/{proxy}" &&
      aws_api_gateway_integration.human["audit/consents/{proxy+} POST"].uri == "http://internal-alb.example.com/v1/audit/consents/{proxy}" &&
      alltrue([for i in aws_api_gateway_integration.human : i.type == "HTTP_PROXY" && i.timeout_milliseconds == 5000])
    )
    error_message = "The sub-route is proxied to /v1/audit/consents/{proxy} of the backend."
  }

  assert {
    condition = (
      keys(aws_api_gateway_method.human_options) == ["audit/consents/{proxy+}"] &&
      aws_api_gateway_method.human_options["audit/consents/{proxy+}"].authorization == "NONE" &&
      aws_api_gateway_integration_response.human_options["audit/consents/{proxy+}"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'"
    )
    error_message = "The sub-route has a CORS preflight without authorization."
  }

  assert {
    condition = (
      aws_api_gateway_method.partner["audit/consents POST"].resource_id == aws_api_gateway_resource.partner_l2["audit/consents"].id &&
      aws_api_gateway_method.partner["audit/consents POST"].api_key_required == true &&
      aws_api_gateway_method.partner["audit/consents POST"].authorization == "NONE"
    )
    error_message = "POST /v1/audit/consents stays a partner route with API key."
  }

  assert {
    condition = (
      length(aws_api_gateway_resource.partner_l1) == 1 && keys(aws_api_gateway_resource.partner_l1) == ["audit"] &&
      length(aws_api_gateway_method.partner) == 2 &&
      !contains(keys(aws_api_gateway_resource.human), "audit")
    )
    error_message = "audit is not a human route: no resource exists for audit/consent-verifications or audit/decisions."
  }
}

run "human_subroutes_do_not_change_the_human_routes" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { consents = { methods = ["GET", "POST"], root_methods = ["GET"] } }
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET", "POST"] } }
  }

  assert {
    condition = toset(keys(aws_api_gateway_method.human)) == toset([
      "consents GET", "consents/{proxy+} GET", "consents/{proxy+} POST",
      "audit/consents/{proxy+} GET", "audit/consents/{proxy+} POST",
    ])
    error_message = "The human routes keep their methods; the sub-route adds its own."
  }
}

run "human_subroutes_reject_a_key_that_is_not_a_partner_route" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/other" = { methods = ["GET"] } }
  }

  expect_failures = [aws_api_gateway_resource.human_subproxy]
}

run "human_subroutes_reject_the_internal_routes_of_audit" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_subroutes = {
      "audit/consent-verifications" = { methods = ["GET"] }
      "audit/decisions"             = { methods = ["GET"] }
    }
  }

  expect_failures = [var.human_subroutes]
}

run "human_subroutes_reject_a_human_route_key" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    human_routes           = { offers = { methods = ["GET", "POST"], root_methods = [] } }
    partner_routes         = { offers = { methods = ["POST"] } }
    human_subroutes        = { offers = { methods = ["GET"] } }
  }

  expect_failures = [aws_api_gateway_resource.human_subproxy]
}

run "human_subroutes_need_the_authorizer" {
  command = plan

  variables {
    partner_routes  = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes = { "audit/consents" = { methods = ["GET"] } }
  }

  expect_failures = [aws_api_gateway_resource.human_subproxy]
}

run "human_subroutes_reject_repeated_or_unknown_methods" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET", "GET"] } }
  }

  expect_failures = [var.human_subroutes]
}

# --- The list of the compliance portal on the exact path (HU-W4.2-01) -------------------

run "human_subroutes_root_methods_publish_the_exact_get_with_cognito" {
  command = apply

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes = {
      "audit/consent-terms/current" = { methods = ["GET"] }
      "audit/consents"              = { methods = ["POST"] }
    }
    human_subroutes = { "audit/consents" = { methods = ["GET", "POST"], root_methods = ["GET"] } }
  }

  assert {
    condition = (
      aws_api_gateway_method.human["audit/consents GET"].resource_id == aws_api_gateway_resource.partner_l2["audit/consents"].id &&
      aws_api_gateway_method.human["audit/consents GET"].authorization == "COGNITO_USER_POOLS" &&
      aws_api_gateway_method.human["audit/consents GET"].authorizer_id != null &&
      aws_api_gateway_method.human["audit/consents GET"].api_key_required == false &&
      aws_api_gateway_method.human["audit/consents GET"].request_parameters == null
    )
    error_message = "GET /v1/audit/consents is on the resource of the partner route, with the Cognito authorizer and no API key."
  }

  assert {
    condition = (
      aws_api_gateway_integration.human["audit/consents GET"].uri == "http://internal-alb.example.com/v1/audit/consents" &&
      aws_api_gateway_integration.human["audit/consents GET"].type == "HTTP_PROXY" &&
      aws_api_gateway_integration.human["audit/consents GET"].integration_http_method == "GET"
    )
    error_message = "The exact GET is proxied to /v1/audit/consents of the backend."
  }

  assert {
    condition = (
      aws_api_gateway_method.partner["audit/consents POST"].resource_id == aws_api_gateway_resource.partner_l2["audit/consents"].id &&
      aws_api_gateway_method.partner["audit/consents POST"].api_key_required == true &&
      aws_api_gateway_method.partner["audit/consents POST"].authorization == "NONE" &&
      length(aws_api_gateway_method.partner) == 2
    )
    error_message = "POST /v1/audit/consents stays a partner route with API key and without the authorizer."
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human)) == toset(["audit/consents GET", "audit/consents/{proxy+} GET", "audit/consents/{proxy+} POST"]) &&
      aws_api_gateway_integration.human["audit/consents/{proxy+} GET"].uri == "http://internal-alb.example.com/v1/audit/consents/{proxy}" &&
      keys(aws_api_gateway_resource.human_subproxy) == ["audit/consents"]
    )
    error_message = "The {proxy+} of HU-W4.2-02 keeps its methods; only the exact GET is added."
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human_options)) == toset(["audit/consents", "audit/consents/{proxy+}"]) &&
      aws_api_gateway_method.human_options["audit/consents"].authorization == "NONE" &&
      aws_api_gateway_integration_response.human_options["audit/consents"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,OPTIONS'" &&
      aws_api_gateway_integration_response.human_options["audit/consents/{proxy+}"].response_parameters["method.response.header.Access-Control-Allow-Methods"] == "'GET,POST,OPTIONS'"
    )
    error_message = "The exact path gets a preflight for its human methods only."
  }

  assert {
    condition = (
      !contains(keys(aws_api_gateway_resource.human), "audit") &&
      keys(aws_api_gateway_resource.partner_l1) == ["audit"]
    )
    error_message = "audit is still not a human route: audit/consent-verifications and audit/decisions do not exist."
  }
}

run "human_subroutes_without_root_methods_add_nothing_on_the_exact_path" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET", "POST"] } }
  }

  assert {
    condition = (
      toset(keys(aws_api_gateway_method.human)) == toset(["audit/consents/{proxy+} GET", "audit/consents/{proxy+} POST"]) &&
      keys(aws_api_gateway_method.human_options) == ["audit/consents/{proxy+}"] &&
      length(aws_api_gateway_method.partner) == 1
    )
    error_message = "Without root_methods the module plans exactly what HU-W4.2-02 planned."
  }
}

run "human_subroutes_reject_a_root_method_of_the_partner" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET"], root_methods = ["POST"] } }
  }

  expect_failures = [aws_api_gateway_method.partner]
}

run "human_subroutes_reject_repeated_root_methods" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET"], root_methods = ["GET", "GET"] } }
  }

  expect_failures = [var.human_subroutes]
}

run "human_subroutes_reject_unknown_root_methods" {
  command = plan

  variables {
    cognito_user_pool_arns = ["arn:aws:cognito-idp:us-east-2:123456789012:userpool/us-east-2_Example"]
    partner_routes         = { "audit/consents" = { methods = ["POST"] } }
    human_subroutes        = { "audit/consents" = { methods = ["GET"], root_methods = ["HEAD"] } }
  }

  expect_failures = [var.human_subroutes]
}
