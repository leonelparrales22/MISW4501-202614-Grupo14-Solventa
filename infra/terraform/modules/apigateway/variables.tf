variable "name" {
  description = "Name of the REST API (for example solventa-staging-ha-l01)."
  type        = string
}

variable "stage_name" {
  description = "Name of the deployment stage."
  type        = string
  default     = "staging"
}

variable "backend_base_url" {
  description = "Base URL of the origination service as seen by the integration, without a trailing slash (for example http://internal-alb.example.com). With a VPC link V2 it sets the Host header and the path; the request reaches integration_target."
  type        = string
}

variable "vpc_link_v2_id" {
  description = "ID of the VPC link V2 (aws_apigatewayv2_vpc_link) to the internal load balancer. Null uses a public INTERNET integration."
  type        = string
  default     = null
}

variable "integration_target_arn" {
  description = "ARN of the internal ALB (or NLB) reached through the VPC link V2. Required when vpc_link_v2_id is set."
  type        = string
  default     = null

  validation {
    condition     = var.integration_target_arn == null || can(regex("^arn:aws:elasticloadbalancing:", var.integration_target_arn))
    error_message = "integration_target_arn must be an Elastic Load Balancing ARN."
  }
}

variable "integration_timeout_ms" {
  description = "Integration timeout of the quoting routes (ADR-02 / EXP-D03: 5 s)."
  type        = number
  default     = 5000

  validation {
    condition     = var.integration_timeout_ms >= 50 && var.integration_timeout_ms <= 29000
    error_message = "integration_timeout_ms must be between 50 and 29000."
  }
}

variable "cache_cluster_size" {
  description = "Size in GB of the stage cache (0.5 is the smallest)."
  type        = string
  default     = "0.5"
}

variable "cache_ttl_seconds" {
  description = "TTL of the cached GET /v1/risk-profiles/{customer_id} answer (decision D-6: 60 s; consent revocation must take effect in 5 minutes at most)."
  type        = number
  default     = 60

  validation {
    condition     = var.cache_ttl_seconds > 0 && var.cache_ttl_seconds <= 300
    error_message = "cache_ttl_seconds must be between 1 and 300 (consent revocation, HU-W1.2-01 CA-5)."
  }
}

variable "partners" {
  description = "Partners with their own API key and usage plan. Rate and burst are requests per second (decision D-7: 100 req/s, burst 200)."
  type = map(object({
    rate_limit  = number
    burst_limit = number
  }))
  default = {
    socio-a = { rate_limit = 100, burst_limit = 200 }
    socio-b = { rate_limit = 100, burst_limit = 200 }
  }
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}

# --- Human channels (HA-S01) -----------------------------------------------------------

variable "cognito_user_pool_arns" {
  description = "ARNs of the Cognito user pools of the human channels (output user_pool_arn of modules/cognito). Empty: no authorizer and no human routes (partner-only API)."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for arn in var.cognito_user_pool_arns : can(regex("^arn:aws:cognito-idp:", arn))])
    error_message = "cognito_user_pool_arns must contain Cognito user pool ARNs."
  }
}

variable "human_routes" {
  description = "Human-channel routes (portal web and mobile app) behind the Cognito authorizer. Key: path segment under /v1 (for example quotes); the route and every sub-path (/{proxy+}) are proxied to the same path of the backend. Each resource with methods gets a CORS preflight (OPTIONS) without authorization. root_methods (T5) sets the methods of the root /v1/<route> on its own: null means the same as methods; [] leaves the root without human methods and without preflight, for example when the root belongs to a partner route (partner_routes)."
  type = map(object({
    methods      = list(string)
    root_methods = optional(list(string))
  }))
  default = {}

  validation {
    condition = alltrue([
      for r in values(var.human_routes) : r.root_methods == null || alltrue([for m in coalesce(r.root_methods, []) : contains(["GET", "POST", "PUT", "PATCH", "DELETE"], m)])
    ])
    error_message = "root_methods of a human route must be among GET, POST, PUT, PATCH and DELETE (it can be empty)."
  }

  validation {
    condition = alltrue([
      for r in values(var.human_routes) : length(r.methods) > 0 && alltrue([for m in r.methods : contains(["GET", "POST", "PUT", "PATCH", "DELETE"], m)])
    ])
    error_message = "Each human route needs at least one method among GET, POST, PUT, PATCH and DELETE (OPTIONS is added automatically, without authorization)."
  }

  validation {
    condition     = alltrue([for k in keys(var.human_routes) : can(regex("^[a-z0-9][a-z0-9-]*$", k))])
    error_message = "Human route keys must be a single lowercase path segment (for example quotes)."
  }

  validation {
    condition     = !contains(keys(var.human_routes), "risk-profiles")
    error_message = "risk-profiles is the partner quoting journey (API key only); it cannot be a human route (CA-5)."
  }
}

# --- Partner routes on explicit paths (T5) ---------------------------------------------

variable "partner_routes" {
  description = "Partner-channel routes on exact paths under /v1 (key: 1 to 3 lowercase segments, for example offers, audit/consents or audit/consent-terms/current), published like the quoting journey: API key of the partner (its usage plan applies), no authorizer, no preflight, HTTP_PROXY to the same path of the backend. A key whose first segment is a human route must be that segment alone, and then it takes the root of that human route (set its root_methods so they do not repeat). Empty: nothing is created and nothing changes."
  type = map(object({
    methods = list(string)
  }))
  default = {}

  validation {
    condition     = alltrue([for k in keys(var.partner_routes) : can(regex("^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*){0,2}$", k))])
    error_message = "partner_routes keys must be 1 to 3 lowercase path segments under /v1 (for example audit/consents)."
  }

  validation {
    condition = alltrue([
      for r in values(var.partner_routes) : length(r.methods) > 0 && length(distinct(r.methods)) == length(r.methods) && alltrue([for m in r.methods : contains(["GET", "POST", "PUT", "PATCH", "DELETE"], m)])
    ])
    error_message = "Each partner route needs at least one method among GET, POST, PUT, PATCH and DELETE, without repetitions."
  }

  validation {
    condition     = alltrue([for k in keys(var.partner_routes) : split("/", k)[0] != "risk-profiles"])
    error_message = "risk-profiles is fixed in the module (the quoting journey); it cannot be a partner route."
  }
}

variable "human_subroutes" {
  description = "Human-channel sub-paths under a partner route (HU-W4.2-02). Key: the key of a partner route (for example audit/consents); its {proxy+} resource is created, with the Cognito authorizer, the listed methods and a CORS preflight, proxied to /v1/<key>/{proxy} of the backend. The partner route itself keeps its API key on the exact path. root_methods (HU-W4.2-01, optional, empty by default) adds human methods on that exact path too, with the authorizer, no API key and a preflight, proxied to /v1/<key>; a method cannot be both in root_methods and in the partner route of the same key. Unlike human_routes, this never publishes the first segment as a whole: siblings such as audit/consent-verifications stay unpublished. Empty: nothing is created and nothing changes."
  type = map(object({
    methods      = list(string)
    root_methods = optional(list(string), [])
  }))
  default = {}

  validation {
    condition     = alltrue([for k in keys(var.human_subroutes) : can(regex("^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*){0,2}$", k))])
    error_message = "human_subroutes keys must be 1 to 3 lowercase path segments under /v1 (for example audit/consents)."
  }

  validation {
    condition = alltrue([
      for r in values(var.human_subroutes) : length(r.methods) > 0 && length(distinct(r.methods)) == length(r.methods) && alltrue([for m in r.methods : contains(["GET", "POST", "PUT", "PATCH", "DELETE"], m)])
    ])
    error_message = "Each human sub-route needs at least one method among GET, POST, PUT, PATCH and DELETE, without repetitions (OPTIONS is added automatically, without authorization)."
  }

  validation {
    condition = alltrue([
      for r in values(var.human_subroutes) : length(distinct(r.root_methods)) == length(r.root_methods) && alltrue([for m in r.root_methods : contains(["GET", "POST", "PUT", "PATCH", "DELETE"], m)])
    ])
    error_message = "root_methods of a human sub-route must be among GET, POST, PUT, PATCH and DELETE, without repetitions (it can be empty)."
  }

  validation {
    condition     = alltrue([for k in keys(var.human_subroutes) : !contains(["audit/consent-verifications", "audit/decisions"], k)])
    error_message = "audit/consent-verifications and audit/decisions are internal to the VPC (no token): they can never be published, not even behind the authorizer."
  }
}

# --- Public routes (HA-D02) ------------------------------------------------------------

variable "public_routes" {
  description = "Read-only routes without session or API key (key: path segment under /v1), for example documents for the signed link of the contractual document (HU-W1.3-03). The route and every sub-path (/{proxy+}) are proxied to the same path of the backend. rate_limit and burst_limit (requests per second) throttle each method at the stage, since no usage plan covers a public route."
  type = map(object({
    methods     = list(string)
    rate_limit  = optional(number)
    burst_limit = optional(number)
  }))
  default = {}

  validation {
    condition     = alltrue([for r in values(var.public_routes) : length(r.methods) > 0 && alltrue([for m in r.methods : m == "GET"])])
    error_message = "Public routes are read-only: their only method is GET."
  }

  validation {
    condition     = alltrue([for k in keys(var.public_routes) : can(regex("^[a-z0-9][a-z0-9-]*$", k))])
    error_message = "Public route keys must be a single lowercase path segment (for example documents)."
  }

  validation {
    condition     = !contains(keys(var.public_routes), "risk-profiles")
    error_message = "risk-profiles is the partner quoting journey (API key only); it cannot be a public route (CA-5)."
  }

  validation {
    condition     = alltrue([for r in values(var.public_routes) : (r.rate_limit == null || r.rate_limit > 0) && (r.burst_limit == null || r.burst_limit > 0)])
    error_message = "rate_limit and burst_limit of a public route must be greater than 0 when set."
  }
}

variable "binary_media_types" {
  description = "Media types the REST API passes as binary, for example [\"*/*\"] so a PDF reaches any client intact whatever its Accept header (HA-D02). With a non-empty list the CORS preflight converts its body to text. Empty keeps the API text only, as before."
  type        = list(string)
  default     = []
}

variable "cors_allow_origin" {
  description = "Value of Access-Control-Allow-Origin in the CORS preflight and the gateway 4XX responses (the web client sends a Bearer token, not cookies). Pin it to the portal origin outside test windows."
  type        = string
  default     = "*"
}

# --- Public card tokenization (T5) -------------------------------------------------------

variable "registration_route" {
  description = "Public POST /v1/registrations and POST /v1/registrations/document-reads routes of the insured sign-up (HU-M1.1-01): the account does not exist yet, so the calls carry no token and no API key. rate_limit and burst_limit (requests per second) throttle each one at the stage, since no usage plan covers a route without API key. backend_path is the path of origination behind the load balancer. timeout_ms is the integration timeout: the sign-up waits for the identity provider (up to 10 s) before creating the account, so it cannot use the 5 s of the quoting routes. Null: nothing is created."
  type = object({
    backend_path = optional(string, "/v1/registrations")
    rate_limit   = optional(number, 2)
    burst_limit  = optional(number, 5)
    timeout_ms   = optional(number, 15000)
  })
  default = null

  validation {
    condition     = var.registration_route == null || can(regex("^/[A-Za-z0-9/_-]*$", var.registration_route.backend_path))
    error_message = "registration_route.backend_path must be an absolute path (for example /v1/registrations)."
  }

  validation {
    condition     = var.registration_route == null || try(var.registration_route.rate_limit > 0 && var.registration_route.burst_limit > 0, false)
    error_message = "registration_route.rate_limit and burst_limit must be greater than 0."
  }

  validation {
    condition     = var.registration_route == null || try(var.registration_route.timeout_ms >= 50 && var.registration_route.timeout_ms <= 29000, false)
    error_message = "registration_route.timeout_ms must be between 50 and 29000 (the limit of a REST API integration)."
  }
}

variable "payment_tokenization" {
  description = "Public POST /pasarela/tokens route of the simulated payment gateway (outside /v1; the name is fixed by the HU-W1.3-02 contract, section 8): the web client sends the card straight to the gateway, so the number never goes through Solventa. No session and no API key; rate_limit and burst_limit (requests per second) throttle it at the stage. backend_path is the path of the gateway behind the load balancer. Null: nothing is created."
  type = object({
    backend_path = optional(string, "/tokens")
    rate_limit   = optional(number, 5)
    burst_limit  = optional(number, 10)
  })
  default = null

  validation {
    condition     = var.payment_tokenization == null || can(regex("^/[A-Za-z0-9/_-]*$", var.payment_tokenization.backend_path))
    error_message = "payment_tokenization.backend_path must be an absolute path (for example /tokens)."
  }

  validation {
    condition     = var.payment_tokenization == null || try(var.payment_tokenization.rate_limit > 0 && var.payment_tokenization.burst_limit > 0, false)
    error_message = "payment_tokenization.rate_limit and burst_limit must be greater than 0."
  }
}
