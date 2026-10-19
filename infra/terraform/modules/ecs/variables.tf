variable "name" {
  description = "Name of the cluster and prefix of its resources (for example solventa-stg)."
  type        = string
}

variable "vpc_id" {
  description = "VPC of the tasks."
  type        = string
}

variable "subnet_ids" {
  description = "Subnets of the tasks (app_subnet_ids of the network module)."
  type        = list(string)
}

variable "assign_public_ip" {
  description = "Give the tasks a public IP (only without NAT, app_assign_public_ip of the network module)."
  type        = bool
}

variable "alb_security_group_id" {
  description = "Security group of the ALB, allowed to reach the ports of the load_balanced services."
  type        = string
  default     = null
}

variable "discovery_namespace" {
  description = "Private DNS namespace of Cloud Map (created only if a service has discovery_name)."
  type        = string
  default     = "solventa.local"
}

variable "services" {
  description = "Long-running services. load_balanced attaches the service to target_group_arn (a static flag, because the ARN is unknown at plan time). secrets maps an environment variable to a Secrets Manager ARN (optionally with :<json key>::). task_role_statements are the AWS permissions of the service."
  type = map(object({
    image            = string
    port             = number
    cpu              = number
    memory           = number
    desired_count    = number
    max_count        = number
    load_balanced    = optional(bool, false)
    target_group_arn = optional(string)
    discovery_name   = optional(string)
    environment      = optional(map(string), {})
    secrets          = optional(map(string), {})
    task_role_statements = optional(list(object({
      sid       = string
      actions   = list(string)
      resources = list(string)
    })), [])
  }))
  default = {}

  validation {
    condition     = alltrue([for s in values(var.services) : s.desired_count >= 0 && s.max_count >= s.desired_count])
    error_message = "desired_count must be >= 0 and max_count >= desired_count."
  }

  validation {
    condition     = alltrue([for s in values(var.services) : s.load_balanced || s.discovery_name != null])
    error_message = "Every service needs load_balanced, a discovery_name or both (otherwise nothing can reach it)."
  }

  validation {
    condition     = alltrue([for s in values(var.services) : !s.load_balanced || s.target_group_arn != null])
    error_message = "A load_balanced service needs its target_group_arn."
  }
}

variable "tasks" {
  description = "One-off task definitions without a service, started with aws ecs run-task."
  type = map(object({
    image       = string
    cpu         = optional(number, 256)
    memory      = optional(number, 512)
    entry_point = optional(list(string))
    command     = optional(list(string))
    environment = optional(map(string), {})
    secrets     = optional(map(string), {})
  }))
  default = {}
}

variable "cpu_target_percent" {
  description = "Average CPU the target tracking keeps (ADR-02: 60)."
  type        = number
  default     = 60

  validation {
    condition     = var.cpu_target_percent > 0 && var.cpu_target_percent <= 100
    error_message = "cpu_target_percent must be between 1 and 100."
  }
}

variable "health_check_grace_period_seconds" {
  description = "Seconds ECS ignores failed ALB health checks of a starting task."
  type        = number
  default     = 30
}

variable "log_retention_days" {
  description = "Retention of the CloudWatch log group of the cluster."
  type        = number
  default     = 7
}

variable "container_insights" {
  description = "Enable Container Insights (extra CloudWatch cost)."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
