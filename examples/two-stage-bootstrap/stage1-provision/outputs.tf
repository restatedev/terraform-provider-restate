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
  value       = restate_cloud_api_key.admin.api_key
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
