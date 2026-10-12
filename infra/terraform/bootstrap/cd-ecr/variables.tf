variable "region" {
  description = "AWS region of the project (Ohio)."
  type        = string
  default     = "us-east-2"

  validation {
    condition     = var.region == "us-east-2"
    error_message = "The project works only in us-east-2; resources in another region are invisible to the team."
  }
}

variable "github_repository" {
  description = "GitHub repository (owner/name) whose workflows may assume the CD role."
  type        = string
  default     = "leonelparrales22/MISW4501-202614-Grupo14-Solventa"
}

variable "github_owner_id" {
  description = "Numeric ID of the GitHub account that owns the repository (gh api repos/<owner>/<name> --jq .owner.id)."
  type        = string
  default     = "52500190"
}

variable "github_repository_id" {
  description = "Numeric ID of the GitHub repository (gh api repos/<owner>/<name> --jq .id)."
  type        = string
  default     = "1325296709"
}

variable "deploy_branches" {
  description = "Branches whose workflow runs may assume the CD role. Pull requests are never allowed."
  type        = set(string)
  default     = ["develop", "main"]
}

variable "create_oidc_provider" {
  description = "Create the GitHub OIDC identity provider. Set to false when the account already has one (an account can hold a single provider per URL) and pass its ARN in existing_oidc_provider_arn."
  type        = bool
  default     = true
}

variable "existing_oidc_provider_arn" {
  description = "ARN of the existing GitHub OIDC provider; only used when create_oidc_provider is false."
  type        = string
  default     = null

  validation {
    condition     = var.create_oidc_provider || var.existing_oidc_provider_arn != null
    error_message = "existing_oidc_provider_arn is required when create_oidc_provider is false."
  }
}

variable "untagged_expiry_days" {
  description = "Days after which untagged images are expired from every repository."
  type        = number
  default     = 7
}

variable "images_to_keep" {
  description = "Number of most recent images kept in every repository."
  type        = number
  default     = 30
}
