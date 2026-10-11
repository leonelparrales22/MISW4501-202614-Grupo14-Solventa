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

variable "name" {
  description = "Prefix of every resource name."
  type        = string
  default     = "solventa-stg-ha-l01"
}

variable "vpc_cidr" {
  description = "CIDR of the window VPC."
  type        = string
  default     = "10.40.0.0/16"
}

variable "image_tag" {
  description = "Tag of the images pushed to the ECR repositories of the window."
  type        = string
  default     = "ha-l01"
}

variable "services_enabled" {
  description = "Run the origination and sources tasks. Apply first with false (creates the ECR repositories), push the images, then apply with true."
  type        = bool
  default     = false
}

variable "k6_enabled" {
  description = "Run the k6 task once (it executes the full sequence and leaves the summary in CloudWatch Logs)."
  type        = bool
  default     = false
}

variable "open_finance_required" {
  description = "OPEN_FINANCE_REQUIRED of the origination service (decision D-2)."
  type        = bool
  default     = true
}

variable "source_latency_profile" {
  description = "Toxiproxy latency profile on the three sources: normal (85 +/- 55 ms) or degradado (150 +/- 120 ms), as in EXP-L01."
  type        = string
  default     = "normal"

  validation {
    condition     = contains(["normal", "degradado"], var.source_latency_profile)
    error_message = "source_latency_profile must be normal or degradado."
  }
}

variable "cache_ttl_seconds" {
  description = "TTL of the cached GET in the API Gateway (decision D-6)."
  type        = number
  default     = 60
}

variable "partners" {
  description = "Test partners with their usage plan (decision D-7: 100 req/s, burst 200)."
  type = map(object({
    rate_limit  = number
    burst_limit = number
  }))
  default = {
    socio-a = { rate_limit = 100, burst_limit = 200 }
    socio-b = { rate_limit = 100, burst_limit = 200 }
  }
}
