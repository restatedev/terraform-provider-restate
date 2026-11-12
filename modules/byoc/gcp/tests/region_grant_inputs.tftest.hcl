mock_provider "google" {
  mock_data "google_project" {
    defaults = { effective_labels = {} }
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

mock_provider "restate" {}

variables {
  project         = "my-restate-region"
  restate_api_key = "key_test"
}

override_resource {
  target = restapi_object.reservation
  values = {
    id           = "rgn_x5z2y4qjmt"
    api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"restate-cloud-region\"}}"
  }
}

run "grant_and_region_are_exclusive" {
  command = plan

  variables {
    region_grant = {}
    restate      = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
  }

  expect_failures = [var.region_grant]
}

run "grant_and_manual_inputs_are_exclusive" {
  command = plan

  variables {
    region_grant   = {}
    restate_issuer = "https://oidc.restate.cloud"
    region_subject = "region:rgn_x5z2y4qjmt"
  }

  expect_failures = [var.region_grant]
}

run "grant_takes_no_stray_region_input" {
  command = plan

  variables {
    region_grant = {}
    region_tag   = "rgn_x5z2y4qjmt"
  }

  expect_failures = [var.region_grant]
}

run "grant_takes_no_location" {
  command = plan

  variables {
    region_grant = {}
    location     = "europe-west1"
  }

  expect_failures = [var.region_grant]
}

run "grant_ignores_a_module_token" {
  command = plan

  variables {
    region_grant         = {}
    restate_api_key      = null
    restate_access_token = "token_test"
  }

  expect_failures = [check.region_grant_credential]
}

run "both_credentials_are_refused" {
  command = plan

  variables {
    restate              = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    location             = "europe-west1"
    restate_access_token = "token_test"
  }

  expect_failures = [var.restate_access_token]
}

run "restate_excludes_the_per_region_inputs" {
  command = plan

  variables {
    restate    = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    location   = "europe-west1"
    region_tag = "rgn_x5z2y4qjmt"
  }

  expect_failures = [var.restate]
}

run "restate_needs_a_credential" {
  command = plan

  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    location        = "europe-west1"
    restate_api_key = null
  }

  expect_failures = [var.restate]
}

run "grant_owner_is_account_or_organization" {
  command = plan

  variables {
    region_grant = { owner = "TEAM" }
  }

  expect_failures = [var.region_grant]
}
