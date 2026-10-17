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
    condition     = alltrue([for k in keys(var.image_tag_overrides) : contains(["origination", "claims", "partners", "audit", "notifications", "openfinance", "opendata", "kyc"], k)])
    error_message = "image_tag_overrides keys must be image names: origination, claims, partners, audit, notifications, openfinance, opendata or kyc."
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
  description = "Tasks of each provider stub (Open Finance, Open Data, KYC)."
  type        = number
  default     = 1
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
  description = "Partners with their own API key and usage plan (decision D-7: 100 req/s, burst 200)."
  type = map(object({
    rate_limit  = number
    burst_limit = number
  }))
  default = {
    socio-a = { rate_limit = 100, burst_limit = 200 }
    socio-b = { rate_limit = 100, burst_limit = 200 }
  }
}

variable "human_routes" {
  description = "Routes of the portal web and mobile app behind the Cognito authorizer (key: path segment under /v1). Default: the authenticated routes of origination."
  type = map(object({
    methods = list(string)
  }))
  default = {
    offers          = { methods = ["GET", "POST"] }
    acceptances     = { methods = ["GET"] }
    assisted-quotes = { methods = ["GET", "POST"] }
    customers       = { methods = ["GET"] }
    policies        = { methods = ["GET"] }
  }
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
  description = "Protect the RDS instance, the ALB and the Cognito user pool against deletion."
  type        = bool
  default     = true
}

variable "ephemeral" {
  description = "Environment destroyed after use: no final RDS snapshot, buckets deleted with their objects and secrets deleted without recovery window."
  type        = bool
  default     = false
}
