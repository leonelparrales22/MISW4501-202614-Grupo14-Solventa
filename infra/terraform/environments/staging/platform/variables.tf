variable "region" {
  description = "AWS region of the project (Ohio)."
  type        = string
  default     = "us-east-2"

  validation {
    condition     = var.region == "us-east-2"
    error_message = "The project works only in us-east-2; resources in another region are invisible to the team."
  }
}

variable "ttl" {
  description = "Date (YYYY-MM-DD) by which everything must be destroyed; it becomes the TTL tag."
  type        = string

  validation {
    condition     = can(regex("^\\d{4}-\\d{2}-\\d{2}$", var.ttl))
    error_message = "ttl must be a date in YYYY-MM-DD format."
  }
}

variable "image_tag" {
  description = "Tag (12-character commit SHA of backend-cd) deployed for every image. null deploys the most recent image of each repository, pinned by digest."
  type        = string
  default     = null
}

variable "image_tag_overrides" {
  description = "Tag per image that overrides image_tag."
  type        = map(string)
  default     = {}
}

variable "services_enabled" {
  description = "Run the ECS tasks (false leaves every service at 0 tasks)."
  type        = bool
  default     = true
}

variable "nat_mode" {
  description = "Internet egress of the tasks: none (default, no NAT cost), single or per_az."
  type        = string
  default     = "none"
}

variable "rds_multi_az" {
  description = "Standby of PostgreSQL in the second zone (false in staging to save cost)."
  type        = bool
  default     = false
}

variable "redis_multi_az" {
  description = "Replica of Redis in the second zone with automatic failover."
  type        = bool
  default     = false
}

variable "cors_allow_origin" {
  description = "Allowed origin of the human routes (* in staging, decision D-12 b of T5)."
  type        = string
  default     = "*"
}

variable "portal_base_url" {
  description = "Origin of the web portal (https://<distribution>.cloudfront.net). Null (default) reads it from the SSM parameter /solventa/staging/web/url that the web composition publishes (T5)."
  type        = string
  default     = null
}

# --- Levers of a test window (HA-D02) ------------------------------------------------

variable "stub_desired_count_overrides" {
  description = "Tasks per stub that override the default of 1: { payment-gateway = 0 } takes the payment gateway down, {} brings it back (HA-D02, decision D-6)."
  type        = map(number)
  default     = {}
}

variable "origination_environment" {
  description = "Extra environment variables of origination for the window, for example a short retry window (RETRY_WINDOW_SECONDS) and a lower longest wait (CHARGE_RETRY_MAX_MS). They cannot replace the values of the platform."
  type        = map(string)
  default     = {}
}

variable "extra_tags" {
  description = "Tags added to the default ones, for example Owner and Story to filter the cost of a window (HA-D02, decision D-7). They cannot replace Project, Env, Task or TTL."
  type        = map(string)
  default     = {}

  validation {
    condition     = length(setintersection(toset(keys(var.extra_tags)), toset(["Project", "Env", "Task", "TTL"]))) == 0
    error_message = "extra_tags cannot replace the default tags Project, Env, Task or TTL."
  }
}
