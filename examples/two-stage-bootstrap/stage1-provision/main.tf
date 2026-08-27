# Stage 1: provision the environment and the credential stage 2 will use.
#
# This state holds a live API key. Use an encrypted, access-controlled backend.

terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

# account_id and management_api_key come from RESTATE_CLOUD_ACCOUNT_ID and
# RESTATE_CLOUD_MANAGEMENT_API_KEY, so no credential appears in this configuration.
provider "restate" {}

# Fail the plan, rather than the apply, if the account cannot use the region asked for. An
# environment's region cannot be changed afterwards: getting it wrong means replacing the
# environment, data included.
data "restate_cloud_regions" "available" {}

check "region_is_available" {
  assert {
    condition     = contains(data.restate_cloud_regions.available.names, var.region)
    error_message = "Region ${var.region} is not available to this account. Available: ${join(", ", data.restate_cloud_regions.available.names)}."
  }
}

# A tier change on a Stripe-billed account can require completing a browser payment flow, which
# Terraform cannot do. Surface that now rather than mid-apply.
data "restate_cloud_account" "current" {}

check "tier_changes_are_non_interactive" {
  assert {
    condition     = data.restate_cloud_account.current.billing_mode != "stripe"
    error_message = "This account is billed through Stripe, so changing tier_id may require a browser payment flow. Change the tier in the Restate Cloud console, or switch the account to account billing."
  }
}

resource "restate_cloud_environment" "this" {
  name    = var.environment_name
  region  = var.region
  tier_id = var.tier_id

  # Destroying an environment is irreversible and takes its data with it. Clearing this guard is a
  # separate, deliberate apply -- which is why the teardown instructions pass -var unprotect=true
  # first.
  deletion_protection = !var.unprotect

  timeouts {
    create = "30m"
    delete = "30m"
  }
}

# The credential stage 2 authenticates with. AdminAccess can manage configuration but cannot invoke
# services, which is the right scope for a Terraform run.
resource "restate_cloud_api_key" "terraform" {
  environment_id = restate_cloud_environment.this.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform stage 2 (${var.environment_name})"

  # Any change to this resource replaces the key, revoking the old secret. Create the replacement
  # first so stage 2 is never left without a valid credential mid-rotation.
  lifecycle {
    create_before_destroy = true
  }
}

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

# The admin URL is not a secret, so a plain output is fine.
output "admin_url" {
  description = "Admin API base URL, for stage 2's RESTATE_ADMIN_URL."
  value       = restate_cloud_environment.this.admin_base_url
}

# The key is a live credential. Marking the output sensitive keeps it out of CLI output, but it is
# still stored in plaintext in this state -- which is why the README recommends a secret store for
# anything beyond a trial.
output "admin_api_key" {
  description = "Environment API key, for stage 2's RESTATE_AUTH_TOKEN. Read it with `terraform output -raw admin_api_key`."
  value       = restate_cloud_api_key.terraform.api_key
  sensitive   = true
}

output "environment_id" {
  description = "Environment ID, for `terraform import` and for the restate_cloud_environment data source."
  value       = restate_cloud_environment.this.id
}

# Pass this to your service deployments so they can verify that a request came from Restate.
output "signing_public_key" {
  description = "Public key services use to verify requests from this environment."
  value       = restate_cloud_environment.this.signing_public_key
}

output "ingress_base_url" {
  description = "Base URL for invoking services in this environment."
  value       = restate_cloud_environment.this.ingress_base_url
}
