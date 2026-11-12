# Its own file, so the grant is created with this response: runs in one file share state, and
# Terraform overrides a resource's values only when it creates the resource.
mock_provider "google" {
  mock_data "google_project" {
    defaults = { effective_labels = {} }
  }
  mock_resource "google_iam_workload_identity_pool" {
    defaults = { name = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt" }
  }
  mock_resource "google_iam_workload_identity_pool_provider" {
    defaults = { name = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt/providers/restate-grant" }
  }
  mock_resource "google_service_account" {
    defaults = {
      name   = "projects/my-restate-identity/serviceAccounts/restate-rgr-x5z2y4qjmt@my-restate-identity.iam.gserviceaccount.com"
      email  = "restate-rgr-x5z2y4qjmt@my-restate-identity.iam.gserviceaccount.com"
      member = "serviceAccount:restate-rgr-x5z2y4qjmt@my-restate-identity.iam.gserviceaccount.com"
    }
  }
}

mock_provider "restapi" {}

mock_provider "restapi" {
  alias = "gcp"
}

mock_provider "restate" {
  mock_resource "restate_cloud_region_grant" {
    defaults = {
      id              = "rgr_x5z2y4qjmt"
      owner_id        = "org_1a2b3c4d5e"
      status          = "PENDING"
      restate_issuer  = "https://oidc.dev.restate.cloud"
      grant_subject   = "grant:rgr_x5z2y4qjmt"
      token_audience  = "restate-cloud-region"
      resource_prefix = "rgr-x5z2y4qjmt"
    }
  }
  mock_resource "restate_cloud_region_grant_confirmation" {
    defaults = { id = "rgr_x5z2y4qjmt", status = "READY" }
  }
}

variables {
  project          = "my-restate-region"
  identity_project = "my-restate-identity"
  # terraform test fails an apply run whose ephemeral variable is unset, so the key is set here
  # even for the runs that do not use it.
  restate_api_key = "key_unused"
}

# The module takes its Restate credential from the caller's restate provider, so a token passed to
# the module is ignored, and the check says so.
run "organization_grant_ignores_a_module_token" {
  command = plan

  variables {
    region_grant         = { owner = "ORGANIZATION", display_name = "Acme GCP" }
    restate_api_key      = null
    restate_access_token = "token_test"
  }

  assert {
    condition     = restate_cloud_region_grant.this[0].owner == "ORGANIZATION" && length(restapi_object.api_key) == 0 && output.restate_api_key == null
    error_message = "A region grant must mint no Management API key."
  }

  expect_failures = [check.region_grant_credential]
}

run "organization_grant_with_an_identity_project" {
  variables {
    region_grant = { owner = "ORGANIZATION", display_name = "Acme GCP" }
  }

  assert {
    condition = (restate_cloud_region_grant.this[0].owner == "ORGANIZATION" && restate_cloud_region_grant.this[0].display_name == "Acme GCP"
      && restate_cloud_region_grant.this[0].cloud == "gcp"
    && restate_cloud_region_grant.this[0].cloud_account_id == "my-restate-region")
    error_message = "An organization grant must be created under its own owner, for the region project."
  }

  assert {
    condition     = restate_cloud_region_grant_confirmation.this[0].grant["identityProject"] == "my-restate-identity" && length(restate_cloud_byoc_region.this) == 0
    error_message = "The confirmation must name the identity project, and no region is built unasked."
  }
  assert {
    condition = (google_iam_workload_identity_pool.restate.workload_identity_pool_id == "restate-rgr-x5z2y4qjmt"
      && google_service_account.grant.account_id == "restate-rgr-x5z2y4qjmt"
    && google_project_service.identity["cloudresourcemanager.googleapis.com"].project == "my-restate-identity")
    error_message = "A shared identity project needs the grant's own pool and account, and Cloud Resource Manager for the grant check."
  }

  assert {
    condition     = google_project_iam_member.grant_storage.condition[0].expression == "resource.name.startsWith('projects/_/buckets/rgr-x5z2y4qjmt-') && resource.name.endsWith('-snapshots-my-restate-region')"
    error_message = "The storage role stays on the region project and keeps the grant's prefix."
  }
}
