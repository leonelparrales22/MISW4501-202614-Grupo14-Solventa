# Configuration tests of the web portal module (T5). They run with a mocked AWS
# provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }

  mock_data "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
    }
  }

  mock_resource "aws_cloudfront_distribution" {
    defaults = {
      id          = "E1234567890ABC"
      arn         = "arn:aws:cloudfront::123456789012:distribution/E1234567890ABC"
      domain_name = "d1234abcd.cloudfront.net"
    }
  }

  # The distribution validates the ARN of its function.
  mock_resource "aws_cloudfront_function" {
    defaults = {
      arn = "arn:aws:cloudfront::123456789012:function/solventa-test-spa-rewrite"
    }
  }

  mock_resource "aws_s3_bucket" {
    defaults = {
      arn                         = "arn:aws:s3:::solventa-test-web-123456789012"
      bucket_regional_domain_name = "solventa-test-web-123456789012.s3.us-east-2.amazonaws.com"
    }
  }
}

# The managed policies are looked up by name. In the provider schema the id of
# the cache and origin request policy data sources is optional, not computed,
# so a mocked provider cannot fill it (it stays null): the runs below check the
# names looked up and the wiring, and the read-only plan of the compositions
# shows the real IDs. The response headers policy id is computed and mockable.
override_data {
  target = data.aws_cloudfront_response_headers_policy.security
  values = { id = "security-headers" }
}

variables {
  name            = "solventa-test"
  environment     = "staging"
  force_destroy   = true
  api             = { domain = "abc123.execute-api.us-east-2.amazonaws.com", stage = "staging" }
  partner_api_key = "mock-partner-key-not-a-real-one"
  cognito         = { region = "us-east-2", user_pool_id = "us-east-2_Example", client_id = "exampleclientid" }
  cd = {
    role_name             = "solventa-test-web"
    github_subject_prefix = "repo:owner@1/front@2"
    branches              = ["develop", "chore/T5-web-portal-hosting"]
  }
}

run "bucket_is_private_and_only_cloudfront_reads_it" {
  command = apply

  assert {
    condition = (
      length(data.aws_iam_policy_document.site_read.statement) == 1 &&
      one(data.aws_iam_policy_document.site_read.statement).effect == "Allow" &&
      one(data.aws_iam_policy_document.site_read.statement).actions == toset(["s3:GetObject"]) &&
      one(data.aws_iam_policy_document.site_read.statement).resources == toset(["arn:aws:s3:::solventa-test-web-123456789012/*"]) &&
      one(one(data.aws_iam_policy_document.site_read.statement).principals).identifiers == toset(["cloudfront.amazonaws.com"])
    )
    error_message = "Only the CloudFront service reads the objects of the site bucket; nobody lists it."
  }

  assert {
    condition = (
      one(one(data.aws_iam_policy_document.site_read.statement).condition).variable == "AWS:SourceArn" &&
      one(one(data.aws_iam_policy_document.site_read.statement).condition).values == tolist([aws_cloudfront_distribution.this.arn])
    )
    error_message = "The read is limited to this distribution (AWS:SourceArn)."
  }

  assert {
    condition = (
      aws_cloudfront_origin_access_control.site.origin_access_control_origin_type == "s3" &&
      aws_cloudfront_origin_access_control.site.signing_behavior == "always" &&
      aws_cloudfront_origin_access_control.site.signing_protocol == "sigv4"
    )
    error_message = "CloudFront signs every request to the bucket (OAC, sigv4, always)."
  }

  assert {
    condition = alltrue([
      for o in aws_cloudfront_distribution.this.origin :
      o.origin_access_control_id == aws_cloudfront_origin_access_control.site.id if o.origin_id == "sitio"
    ])
    error_message = "The site origin uses the origin access control."
  }

  assert {
    condition     = output.bucket == "solventa-test-web-123456789012"
    error_message = "The site bucket is <name>-web-<account id>."
  }
}

run "default_behavior_is_the_site_with_the_spa_function" {
  command = apply

  assert {
    condition = (
      aws_cloudfront_distribution.this.default_cache_behavior[0].target_origin_id == "sitio" &&
      data.aws_cloudfront_cache_policy.optimized.name == "Managed-CachingOptimized" &&
      aws_cloudfront_distribution.this.default_cache_behavior[0].cache_policy_id == data.aws_cloudfront_cache_policy.optimized.id &&
      aws_cloudfront_distribution.this.default_cache_behavior[0].origin_request_policy_id == null &&
      aws_cloudfront_distribution.this.default_cache_behavior[0].response_headers_policy_id == "security-headers" &&
      aws_cloudfront_distribution.this.default_cache_behavior[0].viewer_protocol_policy == "redirect-to-https" &&
      aws_cloudfront_distribution.this.default_cache_behavior[0].compress
    )
    error_message = "Everything that is not the API is the site: cached, compressed, HTTPS only and with the security headers."
  }

  assert {
    condition = (
      one(aws_cloudfront_distribution.this.default_cache_behavior[0].function_association).event_type == "viewer-request" &&
      one(aws_cloudfront_distribution.this.default_cache_behavior[0].function_association).function_arn == aws_cloudfront_function.spa_rewrite.arn
    )
    error_message = "The site behavior runs the deep-link function on every viewer request."
  }

  assert {
    condition = (
      aws_cloudfront_function.spa_rewrite.runtime == "cloudfront-js-2.0" &&
      strcontains(aws_cloudfront_function.spa_rewrite.code, "/index.html") &&
      aws_cloudfront_function.spa_rewrite.publish
    )
    error_message = "The function is published with the cloudfront-js-2.0 runtime and rewrites to /index.html."
  }

  assert {
    condition = (
      aws_cloudfront_distribution.this.default_root_object == "index.html" &&
      aws_cloudfront_distribution.this.http_version == "http2and3" &&
      aws_cloudfront_distribution.this.price_class == "PriceClass_100" &&
      aws_cloudfront_distribution.this.viewer_certificate[0].cloudfront_default_certificate
    )
    error_message = "index.html is the root, with HTTP/3, the cheapest price class and the *.cloudfront.net certificate."
  }
}

run "api_behaviors_never_cache_and_forward_authorization" {
  command = apply

  assert {
    condition = [for b in aws_cloudfront_distribution.this.ordered_cache_behavior : b.path_pattern] == [
      "/v1/risk-profiles/*", "/v1/offers", "/v1/audit/consent-terms/current", "/v1/audit/consents", "/v1/*", "/pasarela/*",
    ]
    error_message = "The exact partner routes go first, then /v1/* and /pasarela/*."
  }

  assert {
    condition = (
      data.aws_cloudfront_cache_policy.disabled.name == "Managed-CachingDisabled" &&
      data.aws_cloudfront_origin_request_policy.all_viewer_except_host.name == "Managed-AllViewerExceptHostHeader" &&
      data.aws_cloudfront_response_headers_policy.security.name == "Managed-SecurityHeadersPolicy"
    )
    error_message = "The behaviors use the managed policies CachingDisabled, AllViewerExceptHostHeader and SecurityHeadersPolicy."
  }

  assert {
    condition = alltrue([
      for b in aws_cloudfront_distribution.this.ordered_cache_behavior :
      b.cache_policy_id == data.aws_cloudfront_cache_policy.disabled.id &&
      b.origin_request_policy_id == data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id &&
      b.response_headers_policy_id == "security-headers" && b.viewer_protocol_policy == "redirect-to-https" &&
      toset(b.allowed_methods) == toset(["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"])
    ])
    error_message = "The API behaviors never cache, forward every viewer header except Host (Authorization included) and accept every method."
  }

  assert {
    condition = alltrue([
      for o in aws_cloudfront_distribution.this.origin :
      o.origin_path == "/staging" && one(o.custom_origin_config).origin_protocol_policy == "https-only" &&
      one(o.custom_origin_config).origin_ssl_protocols == toset(["TLSv1.2"]) &&
      o.domain_name == "abc123.execute-api.us-east-2.amazonaws.com"
      if o.origin_id != "sitio"
    ])
    error_message = "Both API origins are the stage of the REST API over HTTPS with TLS 1.2."
  }
}

run "partner_paths_carry_the_key_and_nothing_else_does" {
  command = apply

  assert {
    condition = (
      tolist([for b in aws_cloudfront_distribution.this.ordered_cache_behavior : b.path_pattern if b.target_origin_id == "api-socio"]) == var.partner_paths &&
      [for b in aws_cloudfront_distribution.this.ordered_cache_behavior : b.path_pattern if b.target_origin_id == "api"] == ["/v1/*", "/pasarela/*"]
    )
    error_message = "Only the partner routes go to the origin that carries the key."
  }

  assert {
    condition = nonsensitive(alltrue([
      for o in aws_cloudfront_distribution.this.origin :
      o.origin_id == "api-socio" ? [for h in o.custom_header : h.name] == ["x-api-key"] : length(o.custom_header) == 0
    ]))
    error_message = "x-api-key is a custom header of the partner origin only."
  }
}

run "partner_key_is_sensitive_in_the_plan" {
  command = plan

  assert {
    condition     = issensitive(var.partner_api_key) && issensitive(aws_cloudfront_distribution.this.origin)
    error_message = "The partner key is sensitive and hides the origins that carry it in the plan."
  }

  assert {
    condition = !anytrue([
      for v in [output.url, output.domain_name, output.distribution_id, output.bucket] : issensitive(v)
    ])
    error_message = "No output exposes the partner key."
  }
}

run "no_custom_error_responses" {
  command = apply

  assert {
    condition     = length(aws_cloudfront_distribution.this.custom_error_response) == 0
    error_message = "No custom error responses: the 401/403/404/5xx of the API must reach the browser untouched."
  }
}

run "config_json_has_no_secret_and_points_to_the_portal" {
  command = apply

  assert {
    condition = (
      aws_s3_object.config[0].key == "config.json" &&
      aws_s3_object.config[0].content_type == "application/json" &&
      aws_s3_object.config[0].cache_control == "no-store"
    )
    error_message = "config.json is JSON and is never cached."
  }

  assert {
    condition = jsondecode(aws_s3_object.config[0].content) == {
      apiGatewayUrl    = "https://d1234abcd.cloudfront.net"
      pasarelaPagosUrl = "https://d1234abcd.cloudfront.net/pasarela"
      cognito = {
        region     = "us-east-2"
        userPoolId = "us-east-2_Example"
        clienteId  = "exampleclientid"
      }
    }
    error_message = "config.json points the API and the payment gateway to the portal itself and carries the user pool and its web client only."
  }

  assert {
    condition     = !strcontains(aws_s3_object.config[0].content, "mock-partner-key-not-a-real-one")
    error_message = "config.json never carries the partner key."
  }
}

run "without_api_only_the_site_exists" {
  command = apply

  variables {
    api             = null
    partner_api_key = null
    cognito         = null
    cd              = null
  }

  assert {
    condition = (
      length(aws_cloudfront_distribution.this.origin) == 1 &&
      length(aws_cloudfront_distribution.this.ordered_cache_behavior) == 0 &&
      length(aws_s3_object.config) == 0
    )
    error_message = "Without api there is only the site: no API origins, no API behaviors and no config.json."
  }

  assert {
    condition     = length(aws_ssm_parameter.site) == 3 && length(aws_iam_role.cd) == 0 && output.cd_role_arn == null
    error_message = "The SSM parameters exist anyway; without cd there is no role."
  }
}

run "api_needs_the_partner_key" {
  command = plan

  variables {
    partner_api_key = null
  }

  expect_failures = [aws_cloudfront_distribution.this]
}

run "cd_role_trusts_only_its_branches_and_cannot_write_config_json" {
  command = apply

  assert {
    condition = (
      aws_iam_role.cd[0].name == "solventa-test-web" &&
      output.cd_role_arn == aws_iam_role.cd[0].arn
    )
    error_message = "The publishing role has the given name."
  }

  assert {
    condition = alltrue([
      for c in one(data.aws_iam_policy_document.cd_trust[0].statement).condition :
      c.variable == "token.actions.githubusercontent.com:sub" ? c.values == tolist([
        "repo:owner@1/front@2:ref:refs/heads/chore/T5-web-portal-hosting",
        "repo:owner@1/front@2:ref:refs/heads/develop",
      ]) : c.values == tolist(["sts.amazonaws.com"])
    ])
    error_message = "The role trusts exactly the given branches of the front-end repository (and the STS audience); never a pull request."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.cd[0].statement :
      s.effect == "Deny" && s.resources == toset(["arn:aws:s3:::solventa-test-web-123456789012/config.json"]) &&
      s.actions == toset(["s3:PutObject", "s3:DeleteObject"])
    ])
    error_message = "The role can never write or delete config.json (it belongs to Terraform)."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.cd[0].statement :
      s.effect != "Deny" && s.resources == toset(["arn:aws:cloudfront::123456789012:distribution/E1234567890ABC"]) &&
      s.actions == toset(["cloudfront:CreateInvalidation", "cloudfront:GetInvalidation"])
    ])
    error_message = "The role invalidates only this distribution."
  }
}

run "ssm_parameters_are_published" {
  command = apply

  assert {
    condition = (
      output.ssm_parameters == {
        bucket          = "/solventa/staging/web/bucket"
        distribution-id = "/solventa/staging/web/distribution-id"
        url             = "/solventa/staging/web/url"
      } &&
      aws_ssm_parameter.site["url"].value == "https://d1234abcd.cloudfront.net" &&
      aws_ssm_parameter.site["distribution-id"].value == "E1234567890ABC" &&
      alltrue([for p in aws_ssm_parameter.site : p.type == "String"])
    )
    error_message = "The bucket, the distribution ID and the URL are published as plain SSM parameters under /solventa/<environment>/web."
  }

  assert {
    condition     = output.url == "https://d1234abcd.cloudfront.net"
    error_message = "The URL of the portal is the HTTPS origin of the distribution."
  }
}

run "rejects_a_wildcard_branch" {
  command = plan

  variables {
    cd = {
      role_name             = "solventa-test-web"
      github_subject_prefix = "repo:owner@1/front@2"
      branches              = ["*"]
    }
  }

  expect_failures = [var.cd]
}
