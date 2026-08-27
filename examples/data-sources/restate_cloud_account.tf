# Read-only account state. The provider does not manage accounts, billing, or subscriptions: those
# operations require an interactive user identity.

data "restate_cloud_account" "current" {}

output "billing_mode" {
  value = data.restate_cloud_account.current.billing_mode
}

output "free_environments_remaining" {
  value = try(
    data.restate_cloud_account.current.free_environment_limit.limit -
    data.restate_cloud_account.current.free_environment_limit.usage,
    null,
  )
}

# A stripe-billed account may be redirected to an interactive Stripe checkout when an environment's
# tier changes, which Terraform cannot complete. Warn at plan time rather than failing mid-apply.
check "tier_changes_are_non_interactive" {
  assert {
    condition     = data.restate_cloud_account.current.billing_mode != "stripe"
    error_message = "This account is billed through Stripe, so a tier change may require completing a browser payment flow. Change the tier in the Restate Cloud console, or switch the account to account billing."
  }
}
