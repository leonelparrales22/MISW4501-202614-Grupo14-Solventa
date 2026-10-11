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
