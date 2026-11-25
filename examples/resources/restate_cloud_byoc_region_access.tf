# Private-only access to an AWS region: no address reaches the load balancer directly, and the
# listed AWS account may connect through an interface endpoint.
#
# An empty entry_source_cidrs also keeps out Restate's control plane, so Restate collects no usage
# from the region's environments, and the Cloud Console's environment views stop working from
# outside your network. Leave the argument out to admit every address.
resource "restate_cloud_byoc_region_access" "production_eu" {
  region_id = restate_cloud_byoc_region.production_eu.id

  entry_source_cidrs = []

  private_link = {
    enabled            = true
    allowed_principals = ["arn:aws:iam::210987654321:root"]
  }

  # The apply waits until AWS has created the endpoint service, within the create timeout.
  timeouts {
    create = "30m"
  }
}

# Known in the same apply, so an aws_vpc_endpoint in your own configuration can use it as its
# service_name. The managed BYOC guide shows one.
output "restate_endpoint_service_name" {
  value = restate_cloud_byoc_region_access.production_eu.endpoint_service_name
}

output "restate_endpoint_availability_zone_ids" {
  value = restate_cloud_byoc_region_access.production_eu.availability_zone_ids
}
