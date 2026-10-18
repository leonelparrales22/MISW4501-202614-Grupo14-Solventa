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

variable "api_enabled" {
  description = "Put the API Gateway of staging/platform on the same domain and write config.json. false keeps only the static site (for a plan before the platform exists, or the dispatch check of the publishing workflow)."
  type        = bool
  default     = true
}

variable "cd_branches" {
  description = "Branches of the front-end repository whose workflow runs may assume the publishing role. develop by default; a staging window can add the branch of its story for the temporary trigger (decision D-9 b of T5), and the role is destroyed with the window."
  type        = list(string)
  default     = ["develop"]

  validation {
    condition     = contains(var.cd_branches, "develop") && alltrue([for b in var.cd_branches : !contains(["main"], b)])
    error_message = "cd_branches must keep develop and never include main (production-demo is published by the main pipeline, #68)."
  }
}

variable "extra_tags" {
  description = "Tags added to the default ones, for example Owner and Story to filter the cost of a window. They cannot replace Project, Env, Task or TTL."
  type        = map(string)
  default     = {}

  validation {
    condition     = length(setintersection(toset(keys(var.extra_tags)), toset(["Project", "Env", "Task", "TTL"]))) == 0
    error_message = "extra_tags cannot replace the default tags Project, Env, Task or TTL."
  }
}
