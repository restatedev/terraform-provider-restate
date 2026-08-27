# The bearer secret is returned when the key is created and never again. It is stored in Terraform
# state, marked sensitive -- so state contains a live credential. Use an encrypted,
# access-controlled backend.

resource "restate_cloud_api_key" "admin" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform stage 2"
}

# An ingress-only key for application traffic, which should not be able to change configuration.
resource "restate_cloud_api_key" "ingress" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::IngressAccess"
  description    = "order service ingress"

  # Any change to this resource replaces the key, which revokes the old secret. Create the
  # replacement first so a running consumer is never left without a valid credential.
  lifecycle {
    create_before_destroy = true
  }
}

resource "restate_cloud_environment" "production" {
  name = "production"
}
