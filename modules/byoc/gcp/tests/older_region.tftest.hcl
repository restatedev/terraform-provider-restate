# Its own file, so the reservation is created with this response. Runs in one file share state,
# and Terraform overrides a resource's values only when it creates the resource.
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

run "older_region_labels_project" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"compositionIssuer\":\"https://oidc.eks.us-east-2.amazonaws.com/id/ABC\",\"regionTag\":\"rgn_x5z2y4qjmt\"}}"
    }
  }

  assert {
    condition = (jsondecode(restapi_object.region_label[0].data).labels == { team = "platform", "restate-cloud-region" = "rgn_x5z2y4qjmt", "restate-cloud-region-rgn_x5z2y4qjmt" = "rgn_x5z2y4qjmt" }
    && jsondecode(restapi_object.region_label[0].destroy_data).labels == { team = "platform" })
    error_message = "An older region must replace a stale plain key, keep the project's other labels, and on destroy delete only its own keys."
  }
}
