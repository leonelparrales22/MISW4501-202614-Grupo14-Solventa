variable "name" {
  description = "Prefix of every resource name (at most 18 characters: solventa-stg, solventa-demo)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,17}$", var.name))
    error_message = "name must start with a letter and have at most 18 lowercase letters, digits and dashes."
  }
}

variable "environment" {
  description = "Environment (also the API Gateway stage name)."
  type        = string

  validation {
    condition     = contains(["staging", "production-demo"], var.environment)
    error_message = "environment must be staging or production-demo."
  }
}

variable "vpc_cidr" {
  description = "CIDR of the VPC (/16)."
  type        = string
}

variable "nat_mode" {
  description = "Internet egress of the tasks: none (public subnets, no NAT cost), single (one shared NAT) or per_az (one NAT per zone, for the demo)."
  type        = string
  default     = "none"
}

# --- Images -----------------------------------------------------------------------------

variable "image_tag" {
  description = "Tag (12-character commit SHA published by backend-cd) deployed for every image. null deploys the most recent image of each repository, pinned by digest."
  type        = string
  default     = null
}

variable "image_tag_overrides" {
  description = "Tag per image that overrides image_tag (backend-cd only rebuilds the images that changed, so a commit may not have every tag)."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for k in keys(var.image_tag_overrides) : contains(["origination", "claims", "partners", "audit", "notifications", "openfinance", "opendata", "kyc", "email", "payment-gateway", "policy-core"], k)])
    error_message = "image_tag_overrides keys must be image names: origination, claims, partners, audit, notifications, openfinance, opendata, kyc, email, payment-gateway or policy-core."
  }
}

# --- Compute ----------------------------------------------------------------------------

variable "services_enabled" {
  description = "Run the ECS tasks. false keeps the rest of the platform (database, cache, gateway) with every service at 0 tasks."
  type        = bool
  default     = true
}

variable "service_sizing" {
  description = "Fargate size and task count of each service. Autoscaling on CPU at 60 % between desired_count and max_count (ADR-02)."
  type = map(object({
    cpu           = number
    memory        = number
    desired_count = number
    max_count     = number
  }))

  validation {
    condition     = toset(keys(var.service_sizing)) == toset(["origination", "claims", "partners", "audit", "notifications"])
    error_message = "service_sizing must have exactly the five services: origination, claims, partners, audit and notifications."
  }
}

variable "stub_desired_count" {
  description = "Tasks of each provider stub (Open Finance, Open Data, KYC, e-mail and payment gateway)."
  type        = number
  default     = 1
}

variable "stub_desired_count_overrides" {
  description = "Tasks per stub that override stub_desired_count. A staging window sets payment-gateway = 0 to take the payment gateway down and an empty map to bring it back (HA-D02, decision D-6); only that ECS service changes."
  type        = map(number)
  default     = {}

  validation {
    condition     = alltrue([for k in keys(var.stub_desired_count_overrides) : contains(["openfinance", "opendata", "kyc", "email", "payment-gateway", "policy-core"], k)])
    error_message = "stub_desired_count_overrides keys must be stub names: openfinance, opendata, kyc, email, payment-gateway or policy-core."
  }

  validation {
    condition     = alltrue([for n in values(var.stub_desired_count_overrides) : n >= 0 && floor(n) == n])
    error_message = "stub_desired_count_overrides values must be whole numbers of tasks, 0 or more."
  }
}

variable "origination_environment" {
  description = "Extra environment variables of origination, for a test window (for example RETRY_WINDOW_SECONDS and CHARGE_RETRY_MAX_MS, decision D-6 of HA-D02). It cannot replace what the platform sets: PORT, REDIS_ADDR, AWS_REGION, DATABASE_URL, any *_URL, AUTH_*, COGNITO_*, DOCUMENTS_*, DOCUMENT_LINK_* or EVENTS_*."
  type        = map(string)
  default     = {}

  validation {
    condition = alltrue([
      for k in keys(var.origination_environment) :
      can(regex("^[A-Z][A-Z0-9_]*$", k)) && !can(regex("^(PORT|REDIS_ADDR|AWS_REGION|DATABASE_URL|.*_URL|AUTH_.*|COGNITO_.*|DOCUMENTS_.*|DOCUMENT_LINK_.*|EVENTS_.*)$", k))
    ])
    error_message = "origination_environment keys must be UPPER_SNAKE_CASE and cannot replace a value of the platform (PORT, REDIS_ADDR, AWS_REGION, DATABASE_URL, *_URL, AUTH_*, COGNITO_*, DOCUMENTS_*, DOCUMENT_LINK_*, EVENTS_*)."
  }
}

variable "log_retention_days" {
  description = "Retention of the CloudWatch logs of the tasks."
  type        = number
  default     = 7
}

# --- Data -------------------------------------------------------------------------------

variable "rds_instance_class" {
  description = "Instance class of PostgreSQL."
  type        = string
  default     = "db.t4g.micro"
}

variable "rds_multi_az" {
  description = "Standby of PostgreSQL in the second zone. false by default (cost); true for the final demo or a fault tolerance test. Switching keeps the data."
  type        = bool
  default     = false
}

variable "rds_backup_retention_days" {
  description = "Days of automated backups of PostgreSQL."
  type        = number
  default     = 1
}

variable "redis_node_type" {
  description = "Node type of Redis."
  type        = string
  default     = "cache.t4g.micro"
}

variable "redis_multi_az" {
  description = "Replica of Redis in the second zone with automatic failover. false by default (cost); true for the final demo."
  type        = bool
  default     = false
}

# --- Edge -------------------------------------------------------------------------------

variable "partners" {
  description = "Partners with their own API key and usage plan (decision D-7: 100 req/s, burst 200). portal-embebido is the demonstration partner whose key CloudFront adds to the partner routes of the public screens of the web portal (T5, decision D-3: 10 req/s, burst 20); the browser never sees it."
  type = map(object({
    rate_limit  = number
    burst_limit = number
  }))
  default = {
    socio-a         = { rate_limit = 100, burst_limit = 200 }
    socio-b         = { rate_limit = 100, burst_limit = 200 }
    portal-embebido = { rate_limit = 10, burst_limit = 20 }
  }
}

variable "portal_base_url" {
  description = "Origin of the web portal of this environment (https://<distribution>.cloudfront.net, without a trailing slash; T5). When set, the e-mail links of origination point to the portal: OFFER_LINK_BASE_URL and DOCUMENTS_PUBLIC_BASE_URL (decision D-11). Null keeps the document links on the API Gateway stage."
  type        = string
  default     = null

  validation {
    condition     = var.portal_base_url == null || can(regex("^https://[^/]+$", var.portal_base_url))
    error_message = "portal_base_url must be an https origin without path or trailing slash (for example https://d1234abcd.cloudfront.net)."
  }
}

variable "portal_partner" {
  description = "Partner of partners whose API key the web portal composition gives to CloudFront (output portal_partner_api_key, T5)."
  type        = string
  default     = "portal-embebido"
}

variable "partner_display_names" {
  description = "Display names of the financial entities the consents authorize, by partner code (HU-M1.3-01, decision D-2 a); audit receives them as PARTNER_DISPLAY_NAMES. A code without a name is shown as it is. Default: the partner of screen E1 of the portal."
  type        = map(string)
  default     = { "aliado-banco-1" = "Banco Aliado" }

  validation {
    condition     = alltrue([for k, v in var.partner_display_names : can(regex("^[A-Za-z0-9_-]+$", k)) && length(trimspace(v)) > 0])
    error_message = "partner_display_names keys are partner codes (letters, digits, - or _) and the names cannot be empty."
  }
}

variable "human_routes" {
  description = "Routes of the portal web and mobile app behind the Cognito authorizer (key: path segment under /v1). Default: the authenticated routes of origination. root_methods (optional) sets the methods of the root /v1/<route> alone; [] gives the root to a partner route (partner_routes)."
  type = map(object({
    methods      = list(string)
    root_methods = optional(list(string))
  }))
  default = {
    # GET /v1/offers/{offer_id}, POST .../renewal and .../acceptance need the
    # session (HU-W1.2-03, HU-W1.3-01). The root POST /v1/offers belongs to the
    # partner (partner_routes), so the root has no human method (T5).
    offers = { methods = ["GET", "POST"], root_methods = [] }
    # POST carries the payment method of screen E4 (HU-W1.3-02) and the
    # resumption of a failed charge by operations (HA-D02); both hang from
    # /v1/acceptances/{id}/charge and the gateway only lets through the
    # methods listed here.
    acceptances     = { methods = ["GET", "POST"] }
    assisted-quotes = { methods = ["GET", "POST"] }
    customers       = { methods = ["GET"] }
    policies        = { methods = ["GET"] }
    # Advisor inbox (HU-W1.1-02): list and read the requests, POST the
    # decision. Issuance exceptions of the premium charge (HA-D02): read by
    # operations; their resumption is POST /v1/acceptances/{id}/charge/resume.
    review-requests     = { methods = ["GET", "POST"] }
    issuance-exceptions = { methods = ["GET"] }
    # Filing of the claim from the guided assistant of the mobile app
    # (HU-M2.2-01) and from the web portal (HU-W2.1-01). GET reads the list of
    # claims (/v1/claims), the filing options, the detail and the filing
    # receipt; POST files the claim, from the root, and uploads the supports
    # and the web filing, from the subroutes.
    claims = { methods = ["GET", "POST"] }
    # The insured reads their own consents from the mobile app (HU-M1.3-01):
    # GET /v1/consents and GET /v1/consents/{consent_id}. The revocation of
    # HU-M1.3-02 adds POST under the consent (POST
    # /v1/consents/{consent_id}/revocations; GET .../revocation-receipt is a
    # GET); the root /v1/consents stays read-only.
    consents = { methods = ["GET", "POST"], root_methods = ["GET"] }
  }
}

variable "partner_routes" {
  description = "Partner-channel routes on exact paths under /v1, with the partner API key and never the authorizer (T5). Default: the consent of HU-W1.2-01 (GET /v1/audit/consent-terms/current and POST /v1/audit/consents, contract HU-W1.2-01 section 2) and the personalized offer of HU-W1.2-03 (POST /v1/offers, contract HU-W1.2-03 section 2). GET /v1/audit/consent-verifications is internal to the VPC and is never published."
  type = map(object({
    methods = list(string)
  }))
  default = {
    offers                        = { methods = ["POST"] }
    "audit/consent-terms/current" = { methods = ["GET"] }
    "audit/consents"              = { methods = ["POST"] }
  }

  validation {
    condition     = !contains(keys(var.partner_routes), "audit/consent-verifications")
    error_message = "The consent verification of audit is internal to the VPC (HU-W1.2-01): /v1/audit/consent-verifications can never be published."
  }
}

variable "human_subroutes" {
  description = "Human-channel sub-paths under a partner route, behind the Cognito authorizer (HU-W4.2-02). Key: the key of a partner route. Default: /v1/audit/consents/{proxy+} with GET and POST, which publishes the evidence of the consent (GET /v1/audit/consents/{consent_id}, HU-W1.2-01, decision D-23 c) and the revocation of the compliance portal (GET .../revocation, POST .../revocations and .../revocation-retries), plus root_methods GET on the exact path for the list of the portal (HU-W4.2-01). Only that {proxy+} and that GET are created: audit is never a human route, because GET /v1/audit/consent-verifications and POST /v1/audit/decisions are internal to the VPC and carry no token."
  type = map(object({
    methods      = list(string)
    root_methods = optional(list(string), [])
  }))
  default = {
    # HU-W4.2-01: root_methods publishes also the list of the compliance portal
    # (GET /v1/audit/consents) with Cognito, next to the POST of the partner.
    "audit/consents" = { methods = ["GET", "POST"], root_methods = ["GET"] }
  }

  validation {
    condition     = alltrue([for k in keys(var.human_subroutes) : !contains(["audit/consent-verifications", "audit/decisions"], k)])
    error_message = "The consent verification and the decision record of audit are internal to the VPC (HU-W1.2-01, HU-W4.1-01): they can never be published, not even behind the authorizer."
  }
}

variable "public_routes" {
  description = "Read-only routes without session or API key (key: path segment under /v1). Default: the signed link of the contractual document, GET /v1/documents/{token} (HU-W1.3-03, HA-D02), throttled at 10 req/s with a burst of 20 (decision D-5), since no usage plan covers it."
  type = map(object({
    methods     = list(string)
    rate_limit  = optional(number)
    burst_limit = optional(number)
  }))
  default = {
    documents = { methods = ["GET"], rate_limit = 10, burst_limit = 20 }
  }
}

variable "existing_cognito" {
  description = "User pool created outside the platform (the permanent one of bootstrap/identity, T4). null creates a user pool for this environment."
  type = object({
    user_pool_id  = string
    user_pool_arn = string
    issuer        = string
    jwks_url      = string
    client_ids    = map(string)
  })
  default = null

  validation {
    condition = var.existing_cognito == null ? true : (
      can(regex("^arn:aws:cognito-idp:[a-z0-9-]+:[0-9]{12}:userpool/", var.existing_cognito.user_pool_arn)) &&
      startswith(var.existing_cognito.issuer, "https://cognito-idp.") &&
      endswith(var.existing_cognito.issuer, "/${var.existing_cognito.user_pool_id}") &&
      var.existing_cognito.jwks_url == "${var.existing_cognito.issuer}/.well-known/jwks.json" &&
      length(var.existing_cognito.client_ids) > 0
    )
    error_message = "existing_cognito must describe a Cognito user pool: its ARN, its issuer (ending in the pool ID), the JWKS URL of that issuer and at least one app client."
  }

  validation {
    condition     = var.existing_cognito == null ? true : (length(var.seed_users) == 0 && var.seed_user_password == null)
    error_message = "Seed users belong to the user pool the platform creates; with existing_cognito, declare the test users where that pool is managed (bootstrap/identity)."
  }
}

variable "seed_users" {
  description = "Test users of the user pool (insured, advisor, operations or partner), so the smoke test of a staging window gets real tokens; only when the platform creates its user pool (existing_cognito = null). Empty by default; production-demo never sets them."
  type = map(object({
    email      = string
    group      = string
    partner_id = optional(string)
  }))
  default = {}
}

variable "seed_user_password" {
  description = "Permanent password of the seed users, only when the platform creates its user pool. Only through TF_VAR_seed_user_password; never in a file."
  type        = string
  default     = null
  sensitive   = true
}

variable "cors_allow_origin" {
  description = "Allowed origin of the human routes (pin it to the portal origin outside test windows)."
  type        = string
  default     = "*"
}

variable "cache_ttl_seconds" {
  description = "TTL of the cached GET /v1/risk-profiles/{customer_id} (decision D-6)."
  type        = number
  default     = 60
}

# --- Lifecycle ------------------------------------------------------------------------

variable "deletion_protection" {
  description = "Protect the RDS instance, the ALB and the Cognito user pool, when the platform creates it, against deletion."
  type        = bool
  default     = true
}

variable "ephemeral" {
  description = "Environment destroyed after use: no final RDS snapshot, buckets deleted with their objects and secrets deleted without recovery window."
  type        = bool
  default     = false
}
