# Regions the configured account may place environments in.
#
# An environment's region cannot be changed after creation, so validating it before an apply avoids
# discovering the mistake as a replacement.

data "restate_cloud_regions" "available" {}

# Fail the plan rather than the apply if the intended region is not available to this account.
check "region_is_available" {
  assert {
    condition     = contains(data.restate_cloud_regions.available.names, var.region)
    error_message = "Region ${var.region} is not available to this account. Available: ${join(", ", data.restate_cloud_regions.available.names)}."
  }
}

resource "restate_cloud_environment" "production" {
  name   = "production"
  region = var.region
}
