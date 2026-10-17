variable "bucket" {
  description = "Globally unique bucket name (for example solventa-stg-documents-<account id>)."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.bucket))
    error_message = "bucket must be a valid S3 bucket name (3-63 lowercase letters, digits, dots and dashes)."
  }
}

variable "force_destroy" {
  description = "Allow destroy to delete a bucket that still holds objects (only ephemeral environments)."
  type        = bool
  default     = false
}

variable "noncurrent_version_expiration_days" {
  description = "Days a previous version of an object is kept."
  type        = number
  default     = 30
}

variable "tags" {
  description = "Tags added to the bucket (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
