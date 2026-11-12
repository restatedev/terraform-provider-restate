# Hands the cloud half of the grant to Restate, which signs in through it. var.aws_grant stands in for
# the grant module's `grant` output, passed through unchanged.
resource "restate_cloud_region_grant_confirmation" "aws" {
  grant_id = restate_cloud_region_grant.aws.id
  grant    = var.aws_grant

  # Restate signs in through the new IAM role in the background while it spreads, and the apply
  # waits until the grant is READY or this timeout passes.
  timeouts {
    create = "5m"
  }
}
