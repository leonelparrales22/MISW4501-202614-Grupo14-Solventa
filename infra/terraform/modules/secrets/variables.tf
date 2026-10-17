variable "prefix" {
  description = "Prefix of the secret names: <prefix>/<key> (for example solventa-stg)."
  type        = string
}

variable "secrets" {
  description = "Secret key => description. The key becomes the name suffix (for example origination/database)."
  type        = map(string)

  validation {
    condition     = alltrue([for k in keys(var.secrets) : can(regex("^[a-z0-9][a-z0-9/_-]*$", k))])
    error_message = "Secret keys must be lowercase letters, digits, slashes, dashes and underscores."
  }
}

variable "values" {
  description = "Secret key => secret string (plain text or JSON). Must hold every key of secrets."
  type        = map(string)
  sensitive   = true
}

variable "recovery_window_in_days" {
  description = "Days a deleted secret can be restored (0 deletes it at once, so an ephemeral environment can be recreated with the same names)."
  type        = number
  default     = 7

  validation {
    condition     = var.recovery_window_in_days == 0 || (var.recovery_window_in_days >= 7 && var.recovery_window_in_days <= 30)
    error_message = "recovery_window_in_days must be 0 or between 7 and 30."
  }
}

variable "tags" {
  description = "Tags added to the secrets (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
