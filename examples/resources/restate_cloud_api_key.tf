# The bearer secret is returned when a key is created and never again, so Terraform state holds the
# only copy. Use an encrypted, access-controlled backend.

# The credential a later Terraform stage configures its Admin API provider with. AdminAccess can
# manage configuration but cannot invoke services, which is the right scope for a Terraform run.
resource "restate_cloud_api_key" "terraform" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform stage 2"
}

# An ingress-only key for application traffic, which should not be able to change configuration.
resource "restate_cloud_api_key" "ingress" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::IngressAccess"
  description    = "order service ingress"

  # Any change to a key replaces it, revoking the old secret. Mint the replacement first so a
  # running consumer is never left without a valid credential.
  lifecycle {
    create_before_destroy = true
  }
}

output "ingress_api_key" {
  value     = restate_cloud_api_key.ingress.api_key
  sensitive = true
}
