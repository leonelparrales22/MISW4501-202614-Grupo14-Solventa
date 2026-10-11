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
  default     = "solventa-stg-ha-s01"
}

variable "seed_users" {
  description = "Test users created by the administrator (decision D-13: permanent password, no invitation). No email is sent."
  type = map(object({
    email      = string
    group      = string
    partner_id = optional(string)
  }))
  default = {
    advisor    = { email = "asesor.ha-s01@example.com", group = "advisor" }
    operations = { email = "operaciones.ha-s01@example.com", group = "operations" }
    partner    = { email = "socio.ha-s01@example.com", group = "partner", partner_id = "socio-a" }
  }
}

variable "seed_user_password" {
  description = "Permanent password of the seed users. Only through TF_VAR_seed_user_password; never in a file."
  type        = string
  sensitive   = true
}
