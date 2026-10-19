variable "name" {
  description = "Prefix of the resource names (solventa-stg, solventa-demo). The site bucket is <name>-web-<account id>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,17}$", var.name))
    error_message = "name must start with a letter and have at most 18 lowercase letters, digits and dashes."
  }
}

variable "environment" {
  description = "Environment of the portal; it names the SSM parameters /solventa/<environment>/web/*."
  type        = string

  validation {
    condition     = contains(["staging", "production-demo"], var.environment)
    error_message = "environment must be staging or production-demo."
  }
}

variable "force_destroy" {
  description = "Allow destroy to delete the site bucket with its objects (only ephemeral environments)."
  type        = bool
  default     = false
}

variable "api" {
  description = "API Gateway of the platform as an origin of the same domain: REST API domain (<id>.execute-api.<region>.amazonaws.com) and stage (the origin path). Null keeps only the static site."
  type = object({
    domain = string
    stage  = string
  })
  default = null

  validation {
    condition = var.api == null || try(
      can(regex("^[a-z0-9]+\\.execute-api\\.[a-z0-9-]+\\.amazonaws\\.com$", var.api.domain)) && can(regex("^[A-Za-z0-9_-]+$", var.api.stage)),
      false,
    )
    error_message = "api.domain must be a REST API domain (<id>.execute-api.<region>.amazonaws.com) and api.stage a stage name."
  }
}

variable "partner_api_key" {
  description = "API key of the demonstration partner of the portal (portal-embebido). CloudFront adds it as x-api-key, overwriting any value of the viewer, only to partner_paths (decision D-3 of T5). Required with api; never reaches the browser."
  type        = string
  default     = null
  sensitive   = true
}

variable "partner_paths" {
  description = "Partner routes called by the public screens of the embedded journey (E1 consent, E2 risk profile and its offer). They go to the API with the key of the portal partner."
  type        = list(string)
  default = [
    "/v1/risk-profiles/*",
    "/v1/offers",
    "/v1/audit/consent-terms/current",
    "/v1/audit/consents",
  ]

  validation {
    condition     = length(var.partner_paths) > 0 && alltrue([for p in var.partner_paths : startswith(p, "/v1/")])
    error_message = "partner_paths must be paths under /v1/."
  }
}

variable "cognito" {
  description = "User pool and web client written to config.json (region, user_pool_id, client_id). With api, config.json is published only when this is set."
  type = object({
    region       = string
    user_pool_id = string
    client_id    = string
  })
  default = null
}

variable "cd" {
  description = "Optional role that the front-end workflow assumes through GitHub OIDC to publish the site: role name, subject prefix of the repository (repo:<owner>@<owner id>/<name>@<repo id>, immutable subject claims) and the branches it trusts. Null creates no role (production-demo, whose publication from main belongs to the main pipeline, #68)."
  type = object({
    role_name             = string
    github_subject_prefix = string
    branches              = list(string)
  })
  default = null

  validation {
    condition = var.cd == null || try(
      can(regex("^repo:[^:*]+$", var.cd.github_subject_prefix)) &&
      length(var.cd.branches) > 0 &&
      alltrue([for b in var.cd.branches : can(regex("^[A-Za-z0-9._/-]+$", b))]),
      false,
    )
    error_message = "cd needs a repo:<owner>@<id>/<name>@<id> subject prefix and at least one branch, without wildcards."
  }
}

variable "price_class" {
  description = "CloudFront price class (PriceClass_100: North America and Europe, the cheapest, decision D-15 of T5)."
  type        = string
  default     = "PriceClass_100"
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
