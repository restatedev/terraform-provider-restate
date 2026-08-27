resource "restate_cloud_environment" "production" {
  name    = "production"
  region  = "us-east-1"
  tier_id = "BUSINESS"

  # Destroying an environment is irreversible and takes its data with it. Clearing this guard is a
  # separate, deliberate apply.
  deletion_protection = true

  # Provisioning is asynchronous. The provider polls until the environment is ACTIVE, so the
  # connection metadata below is populated by the time the apply finishes.
  timeouts {
    create = "30m"
    delete = "30m"
  }
}

# Pass this to your service deployments so they can verify that a request came from Restate.
output "signing_public_key" {
  value = restate_cloud_environment.production.signing_public_key
}

# Configures the Admin API provider of a *later* stage, never one in this apply.
output "admin_base_url" {
  value = restate_cloud_environment.production.admin_base_url
}

output "ingress_base_url" {
  value = restate_cloud_environment.production.ingress_base_url
}
