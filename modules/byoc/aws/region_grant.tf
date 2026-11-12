# With `region_grant` set, one apply creates a region grant in this AWS account and confirms it with
# Restate Cloud, through the caller's restate provider. The grant builds any number of regions in the
# account; `region_grant.regions` builds some here, and administrators of the owning account or
# organization can build more elsewhere with the grant's id.
#
# The order follows the references:
#
# 1. restate_cloud_region_grant answers the grant's id and what the role is built from: Restate's
#    issuer, the subject grant:<id>, the audience and the prefix of every name a region of the grant
#    has in the account.
# 2. main.tf creates the role, its policy and the boundary from those values.
# 3. restate_cloud_region_grant_confirmation makes Restate sign in through the role, which is the
#    proof that the account is the one the grant was created for. Restate keeps trying in the
#    background while IAM spreads the new role, and the confirmation waits until the grant is READY.
# 4. Each region is built with the confirmed grant.
#
# A destroy runs backwards. The regions go first. The confirmation's destroy deletes the grant at
# Restate before the role is touched, and Restate refuses while any region uses the grant, including
# regions built by other configurations, so the destroy stops and keeps the role.

locals {
  region_grant_id = local.region_grant_mode ? restate_cloud_region_grant.this[0].id : null
  region_grant_inputs = !local.region_grant_mode ? null : {
    restateIssuer  = restate_cloud_region_grant.this[0].restate_issuer
    grantSubject   = restate_cloud_region_grant.this[0].grant_subject
    tokenAudience  = restate_cloud_region_grant.this[0].token_audience
    resourcePrefix = restate_cloud_region_grant.this[0].resource_prefix
  }
  grant_output = {
    roleArn             = aws_iam_role.grant.arn
    permissionsBoundary = aws_iam_policy.boundary.arn
  }
}

resource "restate_cloud_region_grant" "this" {
  count = local.region_grant_mode ? 1 : 0

  owner            = var.region_grant.owner
  display_name     = coalesce(var.region_grant.display_name, "aws ${local.account}")
  cloud            = "aws"
  cloud_account_id = local.account
}

# Its grant output references the role, the policy and the boundary, so it is confirmed after them
# and destroyed before them. The policy is named explicitly, because the role's ARN does not change
# when only its policy does.
resource "restate_cloud_region_grant_confirmation" "this" {
  count = local.region_grant_mode ? 1 : 0

  grant_id = restate_cloud_region_grant.this[0].id
  grant    = local.grant_output

  depends_on = [aws_iam_role_policy.grant]
}

resource "restate_cloud_byoc_region" "this" {
  for_each = local.region_grant_mode ? var.region_grant.regions : {}

  name                = each.key
  display_name        = each.value.display_name
  grant_id            = restate_cloud_region_grant_confirmation.this[0].grant_id
  cloud_region        = coalesce(each.value.cloud_region, data.aws_region.current.region)
  deletion_protection = each.value.deletion_protection
}
