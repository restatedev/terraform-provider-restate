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
resource "restate_cloud_api_key" "admin" {
  environment_id = restate_cloud_environment.this.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform stage 2 (${var.environment_name})"

  # Any change to this resource replaces the key, revoking the old secret. Create the replacement
  # first so stage 2 is never left without a valid credential mid-rotation.
  lifecycle {
    create_before_destroy = true
  }
}
