variable "environment_name" {
  description = "Environment name: lowercase letters, digits and hyphens, 2 to 63 characters."
  type        = string
  default     = "example-production"
}

variable "region" {
  description = "Region to place the environment in. Cannot be changed without replacing the environment."
  type        = string
  default     = "us-east-1"
}

variable "tier_id" {
  description = "Environment tier: FREE, STARTER, BUSINESS or PREMIUM."
  type        = string
  default     = "FREE"
}

variable "unprotect" {
  description = <<-EOT
    Clear deletion_protection so the environment can be destroyed.

    Apply this on its own before running destroy. It is a separate apply on purpose: an accidental
    destroy must not be able to clear its own guard.
  EOT
  type        = bool
  default     = false
}
