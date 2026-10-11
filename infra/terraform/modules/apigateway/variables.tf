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
