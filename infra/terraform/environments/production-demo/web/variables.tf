variable "region" {
  description = "AWS region of the project (Ohio)."
  type        = string
  default     = "us-east-2"

  validation {
    condition     = var.region == "us-east-2"
    error_message = "The project works only in us-east-2; resources in another region are invisible to the team."
  }
}

variable "api_enabled" {
  description = "Put the API Gateway of production-demo/platform on the same domain and write config.json. false keeps only the static site (for a plan before the platform exists)."
  type        = bool
  default     = true
}
