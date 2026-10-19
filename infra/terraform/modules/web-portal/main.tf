# Web portal of an environment (T5): the Angular build in a private S3 bucket
# behind CloudFront, with the API Gateway of the platform on the same domain.
#
#   browser --HTTPS--> CloudFront d<id>.cloudfront.net (one domain, no CORS)
#     partner_paths  --> origin "api-socio": API Gateway /<stage> + x-api-key of
#                        the portal partner (CloudFront overwrites the viewer value)
#     /v1/*          --> origin "api": API Gateway /<stage> (Authorization of the
#                        viewer, no Host)
#     /pasarela/*    --> origin "api": public card tokenization of the gateway
#     everything else --> origin "sitio": private S3 bucket through OAC; a viewer
#                        function sends the routes of the SPA to /index.html
#
# The API behaviors never cache and the API errors (401, 403, 404, 5xx) reach
# the browser untouched: there are no custom error responses. config.json (the
# runtime configuration of the front end) is written here and never by the
# publishing workflow. The bucket, distribution and URL are published in SSM,
# where the platform reads the URL for its e-mail links and the publishing
# script finds where to upload.

data "aws_caller_identity" "current" {}

locals {
  account_id  = data.aws_caller_identity.current.account_id
  bucket_name = "${var.name}-web-${local.account_id}"
  ssm_prefix  = "/solventa/${var.environment}/web"
  api_enabled = var.api != null

  site_origin    = "sitio"
  api_origin     = "api"
  partner_origin = "api-socio"

  # Order matters: CloudFront takes the first behavior that matches, so the
  # exact partner routes go before /v1/*.
  api_behaviors = concat(
    [for p in var.partner_paths : { path = p, origin = local.partner_origin }],
    [
      { path = "/v1/*", origin = local.api_origin },
      { path = "/pasarela/*", origin = local.api_origin },
    ],
  )

  url = "https://${aws_cloudfront_distribution.this.domain_name}"
}

# --- Managed policies (by name; no cost) ---------------------------------------------

data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_cache_policy" "optimized" {
  name = "Managed-CachingOptimized"
}

# Every viewer header, cookie and query string except Host (API Gateway needs
# its own Host). With caching disabled, Authorization travels to the origin.
data "aws_cloudfront_origin_request_policy" "all_viewer_except_host" {
  name = "Managed-AllViewerExceptHostHeader"
}

# HSTS, X-Content-Type-Options, X-Frame-Options, Referrer-Policy and
# X-XSS-Protection on every answer (decision D-15 of T5).
data "aws_cloudfront_response_headers_policy" "security" {
  name = "Managed-SecurityHeadersPolicy"
}

# --- Site bucket -----------------------------------------------------------------------

# Only this distribution reads the bucket (OAC signs every request); there is
# no public access and no s3:ListBucket, so a missing file answers 403.
data "aws_iam_policy_document" "site_read" {
  statement {
    sid       = "AllowCloudFrontRead"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${module.bucket.arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

module "bucket" {
  source = "../s3"

  bucket                             = local.bucket_name
  force_destroy                      = var.force_destroy
  noncurrent_version_expiration_days = 7
  extra_policy_json                  = data.aws_iam_policy_document.site_read.json
  tags                               = var.tags
}

resource "aws_cloudfront_origin_access_control" "site" {
  name                              = "${var.name}-web"
  description                       = "Solventa web portal: CloudFront reads the private site bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Deep links of the single-page application: a path whose last segment has no
# dot is a route of the SPA and gets /index.html; files pass through unchanged.
resource "aws_cloudfront_function" "spa_rewrite" {
  name    = "${var.name}-spa-rewrite"
  runtime = "cloudfront-js-2.0"
  comment = "Solventa web portal: SPA routes to /index.html"
  publish = true
  code    = file("${path.module}/functions/spa-rewrite.js")
}

# --- Distribution ----------------------------------------------------------------------

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  comment             = "Solventa web portal (${var.environment})"
  default_root_object = "index.html"
  http_version        = "http2and3"
  is_ipv6_enabled     = true
  price_class         = var.price_class
  tags                = var.tags

  origin {
    origin_id                = local.site_origin
    domain_name              = module.bucket.regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.site.id
  }

  dynamic "origin" {
    for_each = local.api_enabled ? [local.api_origin] : []

    content {
      origin_id   = origin.value
      domain_name = var.api.domain
      origin_path = "/${var.api.stage}"

      custom_origin_config {
        http_port              = 80
        https_port             = 443
        origin_protocol_policy = "https-only"
        origin_ssl_protocols   = ["TLSv1.2"]
      }
    }
  }

  dynamic "origin" {
    for_each = local.api_enabled ? [local.partner_origin] : []

    content {
      origin_id   = origin.value
      domain_name = var.api.domain
      origin_path = "/${var.api.stage}"

      custom_header {
        name  = "x-api-key"
        value = var.partner_api_key
      }

      custom_origin_config {
        http_port              = 80
        https_port             = 443
        origin_protocol_policy = "https-only"
        origin_ssl_protocols   = ["TLSv1.2"]
      }
    }
  }

  dynamic "ordered_cache_behavior" {
    for_each = local.api_enabled ? local.api_behaviors : []

    content {
      path_pattern               = ordered_cache_behavior.value.path
      target_origin_id           = ordered_cache_behavior.value.origin
      allowed_methods            = ["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"]
      cached_methods             = ["GET", "HEAD"]
      viewer_protocol_policy     = "redirect-to-https"
      compress                   = false
      cache_policy_id            = data.aws_cloudfront_cache_policy.disabled.id
      origin_request_policy_id   = data.aws_cloudfront_origin_request_policy.all_viewer_except_host.id
      response_headers_policy_id = data.aws_cloudfront_response_headers_policy.security.id
    }
  }

  default_cache_behavior {
    target_origin_id           = local.site_origin
    allowed_methods            = ["GET", "HEAD"]
    cached_methods             = ["GET", "HEAD"]
    viewer_protocol_policy     = "redirect-to-https"
    compress                   = true
    cache_policy_id            = data.aws_cloudfront_cache_policy.optimized.id
    response_headers_policy_id = data.aws_cloudfront_response_headers_policy.security.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.spa_rewrite.arn
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  # *.cloudfront.net certificate: no custom domain (decision D-15 of T5).
  viewer_certificate {
    cloudfront_default_certificate = true
  }

  lifecycle {
    precondition {
      condition     = !local.api_enabled || nonsensitive(var.partner_api_key != null)
      error_message = "partner_api_key is required with api: the partner routes of the public screens need the key of the portal partner."
    }
  }
}

# --- Runtime configuration of the front end ------------------------------------------

# Public URLs and identifiers only (never the partner key or a secret). The
# front end reads it before starting (decision D-5 of T5); no-store so a new
# pool or URL is picked up at once.
resource "aws_s3_object" "config" {
  count = local.api_enabled && var.cognito != null ? 1 : 0

  bucket        = module.bucket.bucket
  key           = "config.json"
  content_type  = "application/json"
  cache_control = "no-store"
  tags          = var.tags

  content = jsonencode({
    apiGatewayUrl    = local.url
    pasarelaPagosUrl = "${local.url}/pasarela"
    cognito = {
      region     = var.cognito.region
      userPoolId = var.cognito.user_pool_id
      clienteId  = var.cognito.client_id
    }
  })
}

# --- Where the platform and the publishing script find the site ----------------------

resource "aws_ssm_parameter" "site" {
  for_each = {
    bucket          = module.bucket.bucket
    distribution-id = aws_cloudfront_distribution.this.id
    url             = local.url
  }

  name        = "${local.ssm_prefix}/${each.key}"
  description = "Web portal of ${var.environment} (T5): ${each.key}"
  type        = "String"
  value       = each.value
  tags        = var.tags
}

# --- Publishing role of the front-end workflow (optional, decision D-8 of T5) --------

data "aws_iam_openid_connect_provider" "github" {
  count = var.cd != null ? 1 : 0

  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "cd_trust" {
  count = var.cd != null ? 1 : 0

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github[0].arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Exact match on the ref of each trusted branch (never pull requests):
    #   repo:<owner>@<owner_id>/<name>@<repo_id>:ref:refs/heads/<branch>
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [for b in sort(var.cd.branches) : "${var.cd.github_subject_prefix}:ref:refs/heads/${b}"]
    }
  }
}

resource "aws_iam_role" "cd" {
  count = var.cd != null ? 1 : 0

  name                 = var.cd.role_name
  description          = "Assumed by the front-end publishing workflow (GitHub OIDC) to upload the web portal of ${var.environment} and invalidate its distribution."
  assume_role_policy   = data.aws_iam_policy_document.cd_trust[0].json
  max_session_duration = 3600
  tags                 = var.tags
}

# Upload and delete the build, invalidate the distribution and read where they
# are; config.json belongs to Terraform and the role can never write it.
data "aws_iam_policy_document" "cd" {
  count = var.cd != null ? 1 : 0

  statement {
    sid       = "ListTheSiteBucket"
    actions   = ["s3:ListBucket"]
    resources = [module.bucket.arn]
  }

  statement {
    sid       = "WriteTheBuild"
    actions   = ["s3:PutObject", "s3:DeleteObject"]
    resources = ["${module.bucket.arn}/*"]
  }

  statement {
    sid       = "NeverWriteConfigJson"
    effect    = "Deny"
    actions   = ["s3:PutObject", "s3:DeleteObject"]
    resources = ["${module.bucket.arn}/config.json"]
  }

  statement {
    sid       = "InvalidateTheDistribution"
    actions   = ["cloudfront:CreateInvalidation", "cloudfront:GetInvalidation"]
    resources = [aws_cloudfront_distribution.this.arn]
  }

  statement {
    sid       = "ReadWhereTheSiteIs"
    actions   = ["ssm:GetParameter"]
    resources = [for p in aws_ssm_parameter.site : p.arn]
  }
}

resource "aws_iam_role_policy" "cd" {
  count = var.cd != null ? 1 : 0

  name   = "publish-web-portal"
  role   = aws_iam_role.cd[0].id
  policy = data.aws_iam_policy_document.cd[0].json
}
