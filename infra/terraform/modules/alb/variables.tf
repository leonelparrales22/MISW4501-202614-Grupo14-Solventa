variable "name" {
  description = "Name of the ALB and prefix of its target groups (at most 18 characters, so <name>-notifications fits the 32-character limit)."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,17}$", var.name))
    error_message = "name must be at most 18 lowercase letters, digits and dashes."
  }
}

variable "vpc_id" {
  description = "VPC of the ALB."
  type        = string
}

variable "vpc_cidr_block" {
  description = "CIDR of the VPC (egress of the ALB to the targets)."
  type        = string
}

variable "subnet_ids" {
  description = "Subnets of the ALB, in two availability zones."
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "An ALB needs subnets in at least two availability zones."
  }
}

variable "ingress_security_group_ids" {
  description = "Security groups allowed to reach the listener (the API Gateway VPC link), by a static key (for example { vpc_link = <sg id> }; the key keeps for_each known at plan time)."
  type        = map(string)
}

variable "listener_port" {
  description = "Port of the HTTP listener (TLS ends at the API Gateway)."
  type        = number
  default     = 80
}

variable "idle_timeout" {
  description = "Idle timeout in seconds (EXP-D03: 10)."
  type        = number
  default     = 10
}

variable "deregistration_delay" {
  description = "Seconds a draining target keeps serving its in-flight requests (EXP-D03: 30)."
  type        = number
  default     = 30

  validation {
    condition     = var.deregistration_delay >= 0 && var.deregistration_delay <= 3600
    error_message = "deregistration_delay must be between 0 and 3600 seconds."
  }
}

variable "health_check" {
  description = "Health check of every target group (EXP-D03: GET /health every 5 s, 3 s timeout, unhealthy after 2 failures)."
  type = object({
    path                = optional(string, "/health")
    interval            = optional(number, 5)
    timeout             = optional(number, 3)
    healthy_threshold   = optional(number, 2)
    unhealthy_threshold = optional(number, 2)
    matcher             = optional(string, "200")
  })
  default = {}

  validation {
    condition     = var.health_check.timeout < var.health_check.interval
    error_message = "The health check timeout must be shorter than its interval."
  }
}

variable "targets" {
  description = "One target group per service: container port, path patterns routed to it and rule priority (unique, 1-4999)."
  type = map(object({
    port          = number
    path_patterns = list(string)
    priority      = number
  }))

  validation {
    condition     = alltrue([for t in values(var.targets) : length(t.path_patterns) > 0])
    error_message = "Every target needs at least one path pattern (a target group must be attached to the listener)."
  }

  validation {
    condition     = length(distinct([for t in values(var.targets) : t.priority])) == length(var.targets)
    error_message = "Target priorities must be unique."
  }

  validation {
    condition     = alltrue([for t in values(var.targets) : t.priority >= 1 && t.priority <= 4999])
    error_message = "Target priorities must be between 1 and 4999."
  }
}

variable "deletion_protection" {
  description = "Protect the ALB against deletion."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
