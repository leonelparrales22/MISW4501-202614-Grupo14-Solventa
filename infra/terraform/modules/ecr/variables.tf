variable "images" {
  description = "Image names; each one gets the repository <prefix>/<image>."
  type        = set(string)

  validation {
    condition     = alltrue([for i in var.images : can(regex("^[a-z0-9][a-z0-9-]*$", i))])
    error_message = "Image names must be lowercase letters, digits and dashes."
  }
}

variable "prefix" {
  description = "Namespace of the repositories."
  type        = string
  default     = "solventa"
}

variable "image_tag_mutability" {
  description = "IMMUTABLE keeps a commit SHA tag pointing to the same image forever."
  type        = string
  default     = "IMMUTABLE"

  validation {
    condition     = contains(["IMMUTABLE", "MUTABLE"], var.image_tag_mutability)
    error_message = "image_tag_mutability must be IMMUTABLE or MUTABLE."
  }
}

variable "force_delete" {
  description = "Allow destroy to delete repositories that still hold images (only for ephemeral repositories)."
  type        = bool
  default     = false
}

variable "untagged_expiry_days" {
  description = "Days after which untagged images are expired."
  type        = number
  default     = 7
}

variable "images_to_keep" {
  description = "Number of most recent images kept in every repository."
  type        = number
  default     = 30
}
