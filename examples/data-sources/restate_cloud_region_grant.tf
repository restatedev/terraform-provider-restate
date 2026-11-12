# An organization's grant, managed by another configuration, that this account builds regions with.
data "restate_cloud_region_grant" "shared" {
  id = var.region_grant_id
}

check "grant_is_ready" {
  assert {
    condition     = data.restate_cloud_region_grant.shared.status == "READY"
    error_message = "Region grant ${var.region_grant_id} is ${data.restate_cloud_region_grant.shared.status}, so no region can be built with it."
  }
}
