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
  # terraform test fails an apply run whose ephemeral variable is unset, so the key the
  # module ignores without `restate` is set here. A real plan and apply need nothing.
  restate_api_key = "key_unused"
  project         = "my-restate-region"
  location        = "europe-west1"
}

# The trust of a grant made before regions had their own identity, exactly as it was.
run "composition_by_hand" {
  variables {
    region_name        = "prod-eu-x5z2y4qjmt"
    composition_issuer = "https://oidc.eks.us-east-2.amazonaws.com/id/ABC"
    region_tag         = "rgn_x5z2y4qjmt"
  }

  assert {
    condition = (google_iam_workload_identity_pool_provider.composition.workload_identity_pool_provider_id == "composition-cluster"
      && google_iam_workload_identity_pool_provider.composition.attribute_condition == "assertion.sub in [\"system:serviceaccount:crossplane-system:provider-gcp-cloudplatform\",\"system:serviceaccount:crossplane-system:provider-gcp-compute\",\"system:serviceaccount:crossplane-system:provider-gcp-container\",\"system:serviceaccount:crossplane-system:provider-gcp-storage\",\"system:serviceaccount:crossplane-system:prod-eu-x5z2y4qjmt-bootstrap\"]"
      && google_iam_workload_identity_pool_provider.composition.oidc[0].issuer_uri == "https://oidc.eks.us-east-2.amazonaws.com/id/ABC"
    && google_iam_workload_identity_pool_provider.composition.oidc[0].allowed_audiences == tolist(["restate-cloud-composition"]))
    error_message = "The composition cluster's pool provider changed."
  }
  assert {
    condition     = google_service_account_iam_member.federation.member == "principalSet://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/restate-cloud/*"
    error_message = "The pool must keep its impersonation binding."
  }
  assert {
    condition = (jsondecode(restapi_object.region_label[0].data).labels == { team = "platform", "restate-cloud-region" = "rgn_x5z2y4qjmt", "restate-cloud-region-rgn_x5z2y4qjmt" = "rgn_x5z2y4qjmt" }
    && jsondecode(restapi_object.region_label[0].destroy_data).labels == { team = "platform" })
    error_message = "The project must carry both region keys, keep its other labels, and on destroy delete only its own keys."
  }
}

run "region_identity_by_hand" {
  variables {
    restate_issuer = "https://oidc.restate.cloud"
    region_subject = "region:rgn_x5z2y4qjmt"
  }

  assert {
    condition = (google_iam_workload_identity_pool_provider.composition.attribute_condition == "assertion.sub == 'region:rgn_x5z2y4qjmt'"
      && google_iam_workload_identity_pool_provider.composition.oidc[0].issuer_uri == "https://oidc.restate.cloud"
    && google_iam_workload_identity_pool_provider.composition.oidc[0].allowed_audiences == tolist(["restate-cloud-region"]))
    error_message = "The pool provider must accept only the region's subject from Restate's issuer."
  }
  assert {
    condition     = google_service_account_iam_member.federation.member == "principal://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/restate-cloud/subject/region:rgn_x5z2y4qjmt"
    error_message = "Only the region's subject may impersonate the account."
  }
  assert {
    condition     = length(restapi_object.region_label) == 0
    error_message = "A region with its own identity needs no label."
  }
}

run "region_identity_by_hand_shared_identity_project" {
  variables {
    restate_issuer   = "https://oidc.restate.cloud"
    region_subject   = "region:rgn_x5z2y4qjmt"
    identity_project = "my-restate-identity"
  }

  assert {
    condition     = google_service_account.grant.account_id == "restate-rgn-x5z2y4qjmt"
    error_message = "Without a region name, the region's id must name its identity in a shared project."
  }
}
