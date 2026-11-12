mock_provider "google" {
  mock_data "google_project" {
    defaults = { effective_labels = { team = "platform", "restate-cloud-region" = "rgn_stale" } }
  }
  mock_resource "google_iam_workload_identity_pool" {
    defaults = { name = "projects/123456789/locations/global/workloadIdentityPools/restate-cloud" }
  }
  mock_resource "google_service_account" {
    defaults = {
      name   = "projects/my-restate-region/serviceAccounts/restate-cloud-byoc@my-restate-region.iam.gserviceaccount.com"
      email  = "restate-cloud-byoc@my-restate-region.iam.gserviceaccount.com"
      member = "serviceAccount:restate-cloud-byoc@my-restate-region.iam.gserviceaccount.com"
    }
  }
}

mock_provider "restapi" {}

mock_provider "restapi" {
  alias = "gcp"
}

variables {
  project  = "my-restate-region"
  location = "europe-west1"
}

# No resource depends on the identity for its existence, so planning needs no region_identity. Setting it only asks for the identity.
run "restate_new_region" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu", region_identity = true }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
    }
  }

  assert {
    condition = (google_iam_workload_identity_pool_provider.composition.attribute_condition == "assertion.sub == 'region:rgn_x5z2y4qjmt'"
    && jsondecode(restapi_object.region_label[0].data).labels == { team = "platform", "restate-cloud-region" = "rgn_stale" })
    error_message = "A new region must trust its own subject and leave the project's labels as they are."
  }

  assert {
    condition     = jsondecode(restapi_object.reservation[0].data).identity == "region"
    error_message = "A new region must ask CreateRegion for its own identity."
  }

  assert {
    condition     = restapi_object.reservation[0].read_path == "/DescribeRegion" && jsondecode(restapi_object.reservation[0].read_data) == { name = var.restate.name }
    error_message = "The region must be read by its name, since repeating CreateRegion makes a new region once it is deleted."
  }

  assert {
    condition     = restapi_object.teardown[0].destroy_path == "/DeleteRegion" && jsondecode(restapi_object.teardown[0].destroy_data) == { regionId = restapi_object.reservation[0].id }
    error_message = "The teardown must delete the region, which keeps the grant until the region is gone."
  }
}

run "own_project_keeps_fixed_names" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = "key_test"
  }


  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
    }
  }

  assert {
    condition = (google_iam_workload_identity_pool.restate.workload_identity_pool_id == "restate-cloud"
    && google_service_account.grant.account_id == "restate-cloud-byoc")
    error_message = "A region whose identity is in its own project must keep the names it always had."
  }
}

run "shared_identity_project_names_each_region" {
  variables {
    restate          = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key  = "key_test"
    identity_project = "my-restate-identity"
  }


  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
    }
  }

  assert {
    condition = (google_iam_workload_identity_pool.restate.workload_identity_pool_id == "restate-prod-eu"
    && google_service_account.grant.account_id == "restate-prod-eu")
    error_message = "In a shared identity project, each region needs a pool and service account of its own."
  }
}

run "shared_identity_project_long_name" {
  variables {
    restate          = { account_id = "acc_1a2b3c4d5e", name = "production-europe-west-primary" }
    restate_api_key  = "key_test"
    identity_project = "my-restate-identity"
  }


  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
    }
  }

  assert {
    condition = (length(google_service_account.grant.account_id) <= 30
      && can(regex("^restate-production-eur-[0-9a-f]{7}$", google_service_account.grant.account_id))
    && google_iam_workload_identity_pool.restate.workload_identity_pool_id == google_service_account.grant.account_id)
    error_message = "A long region name must fit the 30 characters of a service account ID."
  }
}

run "shared_identity_project_pinned_names" {
  variables {
    restate            = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key    = "key_test"
    identity_project   = "my-restate-identity"
    pool_id            = "restate-cloud"
    service_account_id = "restate-cloud-byoc"
  }


  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
    }
  }

  assert {
    condition = (google_iam_workload_identity_pool.restate.workload_identity_pool_id == "restate-cloud"
    && google_service_account.grant.account_id == "restate-cloud-byoc")
    error_message = "Pinned names must win over the derived ones."
  }
}
