variable "region" {
  description = "AWS region of the project (Ohio)."
  type        = string
  default     = "us-east-2"

  validation {
    condition     = var.region == "us-east-2"
    error_message = "The project works only in us-east-2; resources in another region are invisible to the team."
  }
}

variable "seed_user_password" {
  description = "Permanent password of the four test users. Only through TF_VAR_seed_user_password; never in a file."
  type        = string
  sensitive   = true
  nullable    = false

  validation {
    condition     = length(var.seed_user_password) >= 12
    error_message = "seed_user_password must have at least 12 characters (password policy of the user pool)."
  }
}
