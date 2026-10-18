variable "region" {
  description = "AWS region of the project (Ohio)."
  type        = string
  default     = "us-east-2"

  validation {
    condition     = var.region == "us-east-2"
    error_message = "The project works only in us-east-2; resources in another region are invisible to the team."
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
  description = "Internet egress of the tasks: none (default, no NAT cost), single or per_az (one NAT per zone, for the final demo)."
  type        = string
  default     = "none"
}

variable "rds_multi_az" {
  description = "Standby of PostgreSQL in the second zone. false by default: Multi-AZ on db.t4g.micro exceeds the US$ 20 a month budget if left on; set true only for the final demo or a fault tolerance test (it keeps the data)."
  type        = bool
  default     = false
}

variable "redis_multi_az" {
  description = "Replica of Redis in the second zone with automatic failover (same criterion as rds_multi_az)."
  type        = bool
  default     = false
}

variable "cors_allow_origin" {
  description = "Allowed origin of the human routes. Null (default) pins it to the web portal when it exists (T5, decision D-12 b) and falls back to *."
  type        = string
  default     = null
}

variable "portal_base_url" {
  description = "Origin of the web portal (https://<distribution>.cloudfront.net). Null (default) reads it from the SSM parameter /solventa/production-demo/web/url that the web composition publishes (T5)."
  type        = string
  default     = null
}

variable "deletion_protection" {
  description = "Protect the RDS instance and the ALB against deletion. Only the switch-off command (scripts/environment) sets it to false, to switch production-demo off completely (T5, decision D-18)."
  type        = bool
  default     = true
}

variable "ephemeral" {
  description = "Destroy without final RDS snapshot, with the buckets and their objects and the secrets without recovery window. Only the switch-off command (scripts/environment) sets it to true, so switching on again within 7 days does not collide with the secret names (T5, decision D-18)."
  type        = bool
  default     = false
}
