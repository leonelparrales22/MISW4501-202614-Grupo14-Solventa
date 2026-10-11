variable "name" {
  description = "Name of the user pool and prefix of its app clients (for example solventa-staging-ha-s01)."
  type        = string
}

variable "deletion_protection" {
  description = "Protect the user pool against deletion. Use false only in ephemeral staging windows, so destroy can remove it."
  type        = bool
  default     = true
}

variable "user_pool_tier" {
  description = "Feature plan of the user pool (architecture: Essentials, free up to 10,000 MAU)."
  type        = string
  default     = "ESSENTIALS"

  validation {
    condition     = contains(["LITE", "ESSENTIALS", "PLUS"], var.user_pool_tier)
    error_message = "user_pool_tier must be LITE, ESSENTIALS or PLUS."
  }
}

variable "password_minimum_length" {
  description = "Minimum password length of the user pool."
  type        = number
  default     = 12

  validation {
    condition     = var.password_minimum_length >= 8 && var.password_minimum_length <= 99
    error_message = "password_minimum_length must be between 8 and 99."
  }
}

variable "token_validity_minutes" {
  description = "Validity of the ID and access tokens in minutes (HA-S01 D-6: 5, the Cognito minimum; a revoked token stays valid until it expires)."
  type        = number
  default     = 5

  validation {
    condition     = var.token_validity_minutes >= 5 && var.token_validity_minutes <= 1440
    error_message = "token_validity_minutes must be between 5 and 1440 (Cognito limits)."
  }
}

variable "web_refresh_token_validity_hours" {
  description = "Validity of the refresh token of the web client in hours (HA-S01 D-6: 12)."
  type        = number
  default     = 12

  validation {
    condition     = var.web_refresh_token_validity_hours >= 1 && var.web_refresh_token_validity_hours <= 87600
    error_message = "web_refresh_token_validity_hours must be between 1 and 87600 (Cognito limits: 60 minutes to 10 years)."
  }
}

variable "mobile_refresh_token_validity_days" {
  description = "Validity of the refresh token of the mobile client in days (HA-S01 D-6: 30)."
  type        = number
  default     = 30

  validation {
    condition     = var.mobile_refresh_token_validity_days >= 1 && var.mobile_refresh_token_validity_days <= 3650
    error_message = "mobile_refresh_token_validity_days must be between 1 and 3650 (Cognito limits: up to 10 years)."
  }
}

variable "explicit_auth_flows" {
  description = "Authentication flows of the web and mobile clients (HA-S01 D-5: InitiateAuth with USER_PASSWORD_AUTH and REFRESH_TOKEN_AUTH)."
  type        = list(string)
  default     = ["ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]

  validation {
    condition     = contains(var.explicit_auth_flows, "ALLOW_REFRESH_TOKEN_AUTH")
    error_message = "explicit_auth_flows must include ALLOW_REFRESH_TOKEN_AUTH (session renewal)."
  }

  validation {
    condition     = !contains(var.explicit_auth_flows, "ALLOW_ADMIN_USER_PASSWORD_AUTH")
    error_message = "The public clients must not allow ALLOW_ADMIN_USER_PASSWORD_AUTH."
  }
}

variable "seed_users" {
  description = "Optional test users created by the administrator (staging only). One group per user; partner_id is required for the partner group and forbidden for the others."
  type = map(object({
    email      = string
    group      = string
    partner_id = optional(string)
  }))
  default = {}

  validation {
    condition = alltrue([
      for u in values(var.seed_users) : contains(["insured", "advisor", "operations", "partner"], u.group)
    ])
    error_message = "Each seed user group must be insured, advisor, operations or partner."
  }

  validation {
    condition = alltrue([
      for u in values(var.seed_users) : (u.group == "partner") == (u.partner_id != null)
    ])
    error_message = "partner_id is required for partner users and must be null for the other groups."
  }
}

variable "seed_user_password" {
  description = "Permanent password of the seed users (HA-S01 D-13). Set it only through TF_VAR_seed_user_password; never commit it."
  type        = string
  default     = null
  sensitive   = true
}

variable "tags" {
  description = "Tags added to the user pool (the provider default_tags also apply)."
  type        = map(string)
  default     = {}
}
