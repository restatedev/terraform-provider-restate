# The service account Restate Cloud's composition cluster impersonates to build a BYOC region in
# a project (credentials.source Grant), through workload identity federation. No key exists.
#
# A region signs in in one of two ways, which Restate Cloud chooses when it creates the region:
#
# - With its own identity: a token from Restate's issuer with the audience restate-cloud-region and
#   a subject that names the region. The pool's provider accepts only that issuer, audience and
#   subject, and only that subject may impersonate the account.
# - Through the composition cluster: provider-gcp presents a projected Kubernetes token with the
#   audience restate-cloud-composition. The pool's provider accepts only that audience, only from
#   the composition cluster's issuer, and only for provider-gcp's own ServiceAccounts and the
#   region's bootstrap job. The project's region label binds the grant to the region.
#
# The account, the pool and its provider live in the identity project, the region project unless
# identity_project names another; the account's roles are on the region project. The account
# creates the region's own service accounts, so it holds serviceAccountAdmin and
# serviceAccountUser on the region project. In one project those roles also cover its own IAM
# policy, so it could change its own grant. A separate identity project is the hardened layout.
#
# A region grant (restate.tf, region_grant) is the third way. One grant in the project builds any
# number of regions: the pool's provider accepts only the grant's token, whose subject is
# grant:<grant id>, and only that subject may impersonate the account. The grant has no region
# name, so the snapshot bucket permission matches the grant's resource prefix instead.
#
# The project roles start from what the region's own Crossplane identity holds on the
# ProviderConfig path, defined in restatedev/restate-cloud in
# byoc/functions/go-templating-dir/templates/byoc-infra/gcp/crossplane-controller-identity.yaml.tmpl. storage.admin reaches only
# buckets named like the region's snapshot bucket, and the account may grant no project role
# except the one the composition grants to the nodes.

locals {
  # The audience the composition cluster mounts provider-gcp's token with.
  audience = "restate-cloud-composition"
  region_apis = [
    "cloudresourcemanager.googleapis.com",
    "compute.googleapis.com",
    "container.googleapis.com",
    "iam.googleapis.com",
    "storage.googleapis.com",
  ]
  # Restate's grant check reads the project's permissions through the account, and Google bills that
  # call to the account's own project, so Cloud Resource Manager must be on there too. A grant in
  # the region project has it already.
  identity_apis = concat(
    [
      "iam.googleapis.com",
      "iamcredentials.googleapis.com",
      "sts.googleapis.com",
    ],
    local.grant_mode ? ["cloudresourcemanager.googleapis.com"] : [],
  )
  roles = [
    "roles/container.admin",
    "roles/compute.admin",
    "roles/iam.serviceAccountAdmin",
    "roles/iam.serviceAccountUser",
  ]
  grantable = ["roles/container.defaultNodeServiceAccount"]
  # <region>-snapshots-<project>, as restate.gcpBucketName in restatedev/restate-cloud names it.
  # The region's name is not known here.
  buckets = "-snapshots-${var.project}"
  # A region grant's buckets all start with the grant's prefix, and the composition keeps the
  # prefix whole when it shortens a long name.
  bucket_prefix = local.grant_mode ? "${local.region_grant_inputs.resourcePrefix}-" : ""

  identity_project = coalesce(var.identity_project, var.project)
  # Several regions may share an identity project, and fixed names there would make each region's
  # destroy delete the others' identity. A region grant's names carry its id in either project, because
  # Google Cloud keeps a deleted pool's ID for 30 days, so a grant made again after a destroy, or a
  # project that held a region of the older modes, would otherwise collide. A region's own project
  # holds one region only, so its names stay what they always were.
  identity_key = local.grant_mode ? local.region_grant_inputs.resourcePrefix : (local.identity_project == var.project ? null
    : try(replace(replace(coalesce(
      try(var.restate.name, null), var.region_name, try(trimprefix(var.region_subject, "region:"), null),
  ), "_", "-"), "/-+$/", ""), null))
  # A service account ID is at most 30 characters, so a long region name keeps a hash of the rest.
  identity_name = local.identity_key == null ? null : (length(local.identity_key) <= 22
    ? "restate-${local.identity_key}"
    : "restate-${replace(substr(local.identity_key, 0, 14), "/-+$/", "")}-${substr(sha1(local.identity_key), 0, 7)}"
  )
  pool_id            = coalesce(var.pool_id, local.identity_name, "restate-cloud")
  service_account_id = coalesce(var.service_account_id, local.identity_name, "restate-cloud-byoc")

  # The claim name, which names the region's bootstrap job on the composition cluster
  # (restate.gcpBootstrapName in restatedev/restate-cloud). Taken from the reservation with `restate`.
  region_name        = var.restate != null ? local.grant_inputs.regionName : var.region_name
  composition_issuer = var.restate != null ? try(local.grant_inputs.compositionIssuer, null) : var.composition_issuer
  restate_issuer     = local.grant_mode ? local.region_grant_inputs.restateIssuer : var.restate != null ? try(local.grant_inputs.restateIssuer, null) : var.restate_issuer
  region_subject     = local.grant_mode ? local.region_grant_inputs.grantSubject : var.restate != null ? try(local.grant_inputs.regionSubject, null) : var.region_subject
  region_tag         = var.restate != null ? try(local.grant_inputs.regionTag, null) : var.region_tag
  token_audience     = local.grant_mode ? local.region_grant_inputs.tokenAudience : var.restate != null ? try(local.grant_inputs.tokenAudience, var.token_audience) : var.token_audience
  # restate.region_identity, when set, is known while planning, and the reservation's postcondition
  # holds Restate Cloud's answer to it. Unknown, it would replace the pool's provider, whose id GCP
  # keeps reserved for 30 days after a delete.
  # A region grant always signs in with Restate's issuer.
  region_identity = local.grant_mode || (try(var.restate.region_identity, null) != null ? var.restate.region_identity : local.restate_issuer != null)
  subjects = concat(
    [for sa in var.provider_service_accounts : "system:serviceaccount:crossplane-system:${sa}"],
    local.region_name == null ? [] : ["system:serviceaccount:crossplane-system:${local.region_name}-bootstrap"],
  )
}

resource "google_project_service" "region" {
  for_each           = toset(local.region_apis)
  project            = var.project
  service            = each.value
  disable_on_destroy = false
}

resource "google_project_service" "identity" {
  for_each           = local.identity_project == var.project ? setsubtract(local.identity_apis, local.region_apis) : toset(local.identity_apis)
  project            = local.identity_project
  service            = each.value
  disable_on_destroy = false
}

resource "google_iam_workload_identity_pool" "restate" {
  project                   = local.identity_project
  workload_identity_pool_id = local.pool_id
  display_name              = "Restate Cloud"
  description = (local.grant_mode ? "Restate Cloud, which builds this project's BYOC regions"
    : local.region_identity ? "Restate Cloud, which builds this project's BYOC region"
  : "Restate Cloud's composition cluster, which builds this project's BYOC region")

  depends_on = [google_project_service.identity]

  # The grant's resources are replaced without removing the old ones first (restate.tf).
  lifecycle {
    create_before_destroy = true
  }
}

resource "google_iam_workload_identity_pool_provider" "composition" {
  project                            = local.identity_project
  workload_identity_pool_id          = google_iam_workload_identity_pool.restate.workload_identity_pool_id
  workload_identity_pool_provider_id = local.grant_mode ? "restate-grant" : local.region_identity ? "restate-region" : "composition-cluster"
  display_name                       = local.grant_mode ? "Restate Cloud region grant" : local.region_identity ? "Restate Cloud region" : "Composition cluster"
  attribute_mapping                  = { "google.subject" = "assertion.sub" }
  # Exact subjects, never a prefix: a composition on the composition cluster may create
  # ServiceAccounts in crossplane-system, and could name one provider-gcp-anything. The region's
  # bootstrap job is one more, which signs in to the cluster once to mint its credential. With its
  # own identity, the region has one subject for all of these.
  attribute_condition = local.region_identity ? "assertion.sub == '${local.region_subject}'" : "assertion.sub in ${jsonencode(local.subjects)}"

  oidc {
    issuer_uri        = local.region_identity ? local.restate_issuer : local.composition_issuer
    allowed_audiences = [local.region_identity ? local.token_audience : local.audience]
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_service_account" "grant" {
  project      = local.identity_project
  account_id   = local.service_account_id
  display_name = "Restate Cloud BYOC"
  description  = local.grant_mode ? "Restate Cloud builds the BYOC regions in ${var.project} as this account" : "Restate Cloud builds the BYOC region in ${var.project} as this account"

  depends_on = [google_project_service.identity]

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_service_account_iam_member" "federation" {
  service_account_id = google_service_account.grant.name
  role               = "roles/iam.workloadIdentityUser"
  member = (local.region_identity
    ? "principal://iam.googleapis.com/${google_iam_workload_identity_pool.restate.name}/subject/${local.region_subject}"
    : "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.restate.name}/*"
  )

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_project_iam_member" "grant" {
  for_each = toset(local.roles)
  project  = var.project
  role     = each.value
  member   = google_service_account.grant.member

  depends_on = [google_project_service.region]

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_project_iam_member" "grant_storage" {
  project = var.project
  role    = "roles/storage.admin"
  member  = google_service_account.grant.member

  condition {
    title       = "Region snapshot buckets only"
    description = local.grant_mode ? "Buckets named like the grant's snapshot buckets, and nothing in them" : "Buckets named like the region's snapshot bucket, and nothing in them"
    expression  = "resource.name.startsWith('projects/_/buckets/${local.bucket_prefix}') && resource.name.endsWith('${local.buckets}')"
  }

  depends_on = [google_project_service.region]

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_project_iam_member" "grant_iam" {
  project = var.project
  role    = "roles/resourcemanager.projectIamAdmin"
  member  = google_service_account.grant.member

  condition {
    title       = "Region roles only"
    description = "Grant or revoke only the roles the region composes"
    expression  = "api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', []).hasOnly(${jsonencode(local.grantable)})"
  }

  lifecycle {
    create_before_destroy = true
  }
}
