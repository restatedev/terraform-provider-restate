# The key that registers a BYOK region, which runs in a GKE cluster you build. Create it after the
# cluster exists: it works once and expires four hours after it is created.
resource "restate_cloud_byok_registration_key" "production_us" {
  cloud           = "gcp"
  provider_region = "us-central1"

  # Remove the region at Restate when this resource is destroyed. Restate refuses while the region
  # hosts environments, and the destroy then stops until they are deleted.
  deregister_region_on_destroy = true
}

# The BYOC chart reads the key from a Secret in its release namespace, and takes the region id as a
# value. The gcp-byok module in modules/byoc does both.
output "byok_region_id" {
  value = restate_cloud_byok_registration_key.production_us.region_id
}

output "byok_registration_key" {
  value     = restate_cloud_byok_registration_key.production_us.key
  sensitive = true
}
