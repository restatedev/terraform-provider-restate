# With `region_grant` set, one apply creates a region grant in the project and confirms it with
# Restate Cloud, through the caller's restate provider. The grant builds any number of regions in the
# project; `region_grant.regions` builds some here, and administrators of the owning account or
# organization can build more elsewhere with the grant's id.
#
# The order follows the references:
#
# 1. restate_cloud_region_grant answers the grant's id and what the cloud grant is built from:
#    Restate's issuer, the subject grant:<id>, the audience and the prefix of every name a region of
#    the grant has in the project.
# 2. main.tf creates the pool, the service account and their roles from those values.
# 3. restate_cloud_region_grant_confirmation makes Restate sign in through the grant, which is the
#    proof that the project is the one the grant was created for. Google documents that an IAM change
#    usually takes effect in about two minutes and sometimes in seven or more, so the confirmation is
#    given a timeout well past that. Restate keeps signing in in the background until then, and the
#    confirmation waits until the grant is READY.
# 4. Each region is built with the confirmed grant.
#
# A destroy runs backwards. The regions go first. The confirmation's destroy deletes the grant at
# Restate before the project's grant is touched, and Restate refuses while any region uses the grant,
# including regions built by other configurations, so the destroy stops and keeps the cloud grant.

locals {
  grant_mode = var.region_grant != null

  region_grant_id = local.grant_mode ? restate_cloud_region_grant.this[0].id : null
  region_grant_inputs = !local.grant_mode ? null : {
    restateIssuer  = restate_cloud_region_grant.this[0].restate_issuer
    grantSubject   = restate_cloud_region_grant.this[0].grant_subject
    tokenAudience  = restate_cloud_region_grant.this[0].token_audience
    resourcePrefix = restate_cloud_region_grant.this[0].resource_prefix
  }
  grant_output = {
    workloadIdentityProvider = google_iam_workload_identity_pool_provider.composition.name
    serviceAccount           = google_service_account.grant.email
    identityProject          = local.identity_project
  }
}

resource "restate_cloud_region_grant" "this" {
  count = local.grant_mode ? 1 : 0

  owner            = var.region_grant.owner
  display_name     = var.region_grant.display_name
  cloud            = "gcp"
  cloud_account_id = var.project
}

# Its grant output names the provider and the service account, and the roles below are what the
# sign-in checks, so it is confirmed after all of them and destroyed before them.
resource "restate_cloud_region_grant_confirmation" "this" {
  count = local.grant_mode ? 1 : 0

  grant_id = restate_cloud_region_grant.this[0].id
  grant    = local.grant_output

  timeouts {
    create = "10m"
    update = "10m"
  }

  depends_on = [
    google_iam_workload_identity_pool_provider.composition,
    google_service_account_iam_member.federation,
    google_project_iam_member.grant,
    google_project_iam_member.grant_storage,
    google_project_iam_member.grant_iam,
  ]
}

resource "restate_cloud_byoc_region" "this" {
  for_each = local.grant_mode ? var.region_grant.regions : {}

  name                = each.key
  display_name        = each.value.display_name
  grant_id            = restate_cloud_region_grant_confirmation.this[0].grant_id
  cloud_region        = each.value.cloud_region
  machine_type        = each.value.machine_type
  deletion_protection = each.value.deletion_protection
}
