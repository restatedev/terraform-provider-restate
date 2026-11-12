# A BYOC region in the grant's AWS account. Referencing the confirmation, not the grant, makes the
# region wait until the grant is READY, and makes a destroy delete the region before the grant.
resource "restate_cloud_byoc_region" "production_eu" {
  name         = "production-eu"
  grant_id     = restate_cloud_region_grant_confirmation.aws.grant_id
  cloud_region = "eu-central-1"

  # The default. Deleting a region removes everything Restate built in your account for it, so a
  # destroy, or a change that replaces the region, needs this set to false and applied first.
  deletion_protection = true

  # Building the network, cluster and storage takes a while. The provider polls until the region is
  # ACTIVE.
  timeouts {
    create = "90m"
    delete = "60m"
  }
}
