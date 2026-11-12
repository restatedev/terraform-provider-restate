# A region grant owned by an account, in the region's own project.
mock_provider "google" {
  mock_data "google_project" {
    defaults = { effective_labels = { team = "platform" } }
  }
  mock_resource "google_iam_workload_identity_pool" {
    defaults = { name = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt" }
  }
  mock_resource "google_iam_workload_identity_pool_provider" {
    defaults = { name = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt/providers/restate-grant" }
  }
  mock_resource "google_service_account" {
    defaults = {
      name   = "projects/my-restate-region/serviceAccounts/restate-rgr-x5z2y4qjmt@my-restate-region.iam.gserviceaccount.com"
      email  = "restate-rgr-x5z2y4qjmt@my-restate-region.iam.gserviceaccount.com"
      member = "serviceAccount:restate-rgr-x5z2y4qjmt@my-restate-region.iam.gserviceaccount.com"
    }
  }
}

mock_provider "restapi" {}

mock_provider "restapi" {
  alias = "gcp"
}

# What Restate Cloud answers for a new grant, as the restate provider exports it.
mock_provider "restate" {
  mock_resource "restate_cloud_region_grant" {
    defaults = {
      id              = "rgr_x5z2y4qjmt"
      owner_id        = "acc_1a2b3c4d5e"
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
  mock_resource "restate_cloud_byoc_region" {
    defaults = { id = "rgn_a1b2c3d4e5", status = "ACTIVE" }
  }
}

variables {
  project = "my-restate-region"
  # terraform test fails an apply run whose ephemeral variable is unset, so the key is set here
  # even for the runs that do not use it.
  restate_api_key = "key_unused"
}

run "account_grant" {
  variables {
    region_grant = {
      regions = {
        prod-eu = { cloud_region = "europe-west1" }
        prod-us = { cloud_region = "us-central1", machine_type = "e2-standard-8", deletion_protection = false }
      }
    }
  }

  assert {
    condition = (restate_cloud_region_grant.this[0].owner == "ACCOUNT" && restate_cloud_region_grant.this[0].cloud == "gcp"
      && restate_cloud_region_grant.this[0].cloud_account_id == "my-restate-region"
    && var.region_grant.display_name == null)
    error_message = "The grant must carry the owner, the cloud and the project. With no display name set, Restate chooses one."
  }

  assert {
    condition = (google_iam_workload_identity_pool_provider.composition.workload_identity_pool_provider_id == "restate-grant"
      && google_iam_workload_identity_pool_provider.composition.attribute_condition == "assertion.sub == 'grant:rgr_x5z2y4qjmt'"
      && google_iam_workload_identity_pool_provider.composition.oidc[0].issuer_uri == "https://oidc.dev.restate.cloud"
    && google_iam_workload_identity_pool_provider.composition.oidc[0].allowed_audiences == tolist(["restate-cloud-region"]))
    error_message = "The pool provider must accept only the grant's subject, from Restate's issuer, with the grant's audience."
  }

  assert {
    condition     = google_service_account_iam_member.federation.member == "principal://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt/subject/grant:rgr_x5z2y4qjmt"
    error_message = "Only the grant's subject may act as the service account."
  }

  assert {
    condition = (google_project_iam_member.grant_storage.condition[0].expression == "resource.name.startsWith('projects/_/buckets/rgr-x5z2y4qjmt-') && resource.name.endsWith('-snapshots-my-restate-region')"
    && length(google_project_iam_member.grant) == 4)
    error_message = "Storage Admin must reach only buckets that carry the grant's prefix, and the project roles must be the region grant's."
  }

  assert {
    condition = (google_iam_workload_identity_pool.restate.workload_identity_pool_id == "restate-rgr-x5z2y4qjmt"
    && google_service_account.grant.account_id == "restate-rgr-x5z2y4qjmt")
    error_message = "A grant's pool and service account carry its id in its own project too, because Google Cloud keeps a deleted pool's ID for 30 days and a grant made again would collide."
  }

  assert {
    condition = (restate_cloud_region_grant_confirmation.this[0].grant_id == "rgr_x5z2y4qjmt"
      && restate_cloud_region_grant_confirmation.this[0].grant == tomap({
        workloadIdentityProvider = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt/providers/restate-grant"
        serviceAccount           = "restate-rgr-x5z2y4qjmt@my-restate-region.iam.gserviceaccount.com"
        identityProject          = "my-restate-region"
    }))
    error_message = "The confirmation must carry the grant id and the three keys of the module's grant output, and no resource group or region."
  }

  assert {
    condition = (restate_cloud_region_grant_confirmation.this[0].timeouts.create == "10m"
    && restate_cloud_region_grant_confirmation.this[0].timeouts.update == "10m")
    error_message = "Google Cloud's IAM can take several minutes to spread, so the confirmation must keep trying past that."
  }

  assert {
    condition = (restate_cloud_byoc_region.this["prod-eu"].cloud_region == "europe-west1"
      && restate_cloud_byoc_region.this["prod-us"].cloud_region == "us-central1"
      && restate_cloud_byoc_region.this["prod-eu"].grant_id == "rgr_x5z2y4qjmt"
      && restate_cloud_byoc_region.this["prod-us"].machine_type == "e2-standard-8"
    && restate_cloud_byoc_region.this["prod-eu"].deletion_protection && !restate_cloud_byoc_region.this["prod-us"].deletion_protection)
    error_message = "Each region must be built with the confirmed grant, in its own cloud_region, with the machine type it names."
  }

  assert {
    condition = (output.grant_id == "rgr_x5z2y4qjmt"
      && output.resource_prefix == "rgr-x5z2y4qjmt"
      && output.status == "READY"
      && output.grant == {
        workloadIdentityProvider = "projects/123456789/locations/global/workloadIdentityPools/restate-rgr-x5z2y4qjmt/providers/restate-grant"
        serviceAccount           = "restate-rgr-x5z2y4qjmt@my-restate-region.iam.gserviceaccount.com"
        identityProject          = "my-restate-region"
    })
    error_message = "The outputs must carry the grant's id, prefix and status, and the grant map."
  }

  assert {
    condition     = output.region_ids == { prod-eu = "rgn_a1b2c3d4e5", prod-us = "rgn_a1b2c3d4e5" }
    error_message = "The outputs must carry the regions' ids by name."
  }

  assert {
    condition     = length(restapi_object.region_label) == 0 && length(restapi_object.api_key) == 0 && output.region_id == null
    error_message = "A region grant must label no project, mint no key and create no region of its own mode."
  }
}
