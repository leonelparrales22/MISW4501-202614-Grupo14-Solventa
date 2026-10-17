variable "name" {
  description = "Prefix of the topic and queue names (for example solventa-stg)."
  type        = string
}

variable "topics" {
  description = "One SNS topic per publishing domain (for example origination, claims)."
  type        = set(string)
}

variable "queues" {
  description = "One SQS queue per consumer, with the topics it subscribes to. filter_policy is an optional SNS filter policy (JSON) applied to every subscription of the queue."
  type = map(object({
    topics                     = list(string)
    max_receive_count          = optional(number, 5)
    visibility_timeout_seconds = optional(number, 30)
    filter_policy              = optional(string)
  }))

  validation {
    condition     = alltrue([for q in values(var.queues) : q.max_receive_count >= 1 && q.max_receive_count <= 1000])
    error_message = "max_receive_count must be between 1 and 1000."
  }
}

variable "message_retention_seconds" {
  description = "Retention of the consumer queues (default 4 days)."
  type        = number
  default     = 345600
}

variable "dlq_retention_seconds" {
  description = "Retention of the dead-letter queues (default 14 days, the SQS maximum)."
  type        = number
  default     = 1209600
}

variable "tags" {
  description = "Tags added to the taggable resources (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
