# A BYOK region whose images come straight from Restate's registry proxy, which is the default. Nothing
# here reaches Google Cloud, a cluster or Restate Cloud: every provider is mocked.
mock_provider "google" {
  mock_data "google_client_config" {
    defaults = { access_token = "mock-token" }
  }
  mock_resource "google_container_cluster" {
    defaults = {
      endpoint    = "203.0.113.10"
      location    = "us-central1"
      master_auth = { cluster_ca_certificate = "bW9jay1jYQ==" }
    }
  }
  # The same answer for each of the three accounts, in the shape the provider validates.
  mock_resource "google_service_account" {
    defaults = {
      email  = "restate-mock@my-restate-region.iam.gserviceaccount.com"
      name   = "projects/my-restate-region/serviceAccounts/restate-mock@my-restate-region.iam.gserviceaccount.com"
      member = "serviceAccount:restate-mock@my-restate-region.iam.gserviceaccount.com"
    }
  }
}

mock_provider "kubernetes" {}

mock_provider "helm" {}

mock_provider "time" {}

# What Restate Cloud answers for a new key, as the restate provider exports it, with the pull credential
# that comes with it.
mock_provider "restate" {
  mock_resource "restate_cloud_byok_registration_key" {
    defaults = {
      id         = "rgn_a1b2c3d4e5"
      region_id  = "rgn_a1b2c3d4e5"
      key        = "rrk_1a2b3c.4d5e6f"
      expires_at = "2026-11-18T16:00:00.000Z"
      image_pull = {
        registry          = "registry.dev.restate.cloud"
        username          = "restate"
        password          = "key_pullsecret1234"
        repository_prefix = "registry.dev.restate.cloud/dev-"
      }
    }
  }
}

override_resource {
  target = google_container_cluster.this
  values = { node_locations = ["us-central1-a", "us-central1-b", "us-central1-c"] }
}

variables {
  project           = "my-restate-region"
  region            = "us-central1"
  control_plane_url = "https://api.dev.restate.cloud"
  byoc_chart = {
    chart             = "restate-byoc"
    repository        = "oci://example.test/charts"
    version           = "0.1.0"
    dependency_update = false
  }
}

run "direct_is_the_default" {
  command = plan

  assert {
    condition     = var.image_source_mode == "direct" && var.image_source_registry == ""
    error_message = "Without image_source_mode the module must pull straight from Restate's registry, with no registry of the customer's to name."
  }

  assert {
    condition = (length(kubernetes_secret_v1.image_pull) == 1
      && kubernetes_secret_v1.image_pull[0].metadata[0].name == "restate-registry"
      && kubernetes_secret_v1.image_pull[0].metadata[0].namespace == "restate-system"
    && kubernetes_secret_v1.image_pull[0].type == "kubernetes.io/dockerconfigjson")
    error_message = "Direct mode must create the pull Secret in the chart's namespace, as a docker config, under the name the chart's default expects."
  }

  assert {
    condition     = output.image_registry == "registry.dev.restate.cloud"
    error_message = "The registry must follow from the control plane's domain, as the chart derives it."
  }
}

run "pull_secret_and_chart_values" {
  command = apply

  assert {
    condition = (nonsensitive(jsondecode(kubernetes_secret_v1.image_pull[0].data[".dockerconfigjson"])) == {
      auths = {
        "registry.dev.restate.cloud" = {
          username = "restate"
          password = "key_pullsecret1234"
          auth     = base64encode("restate:key_pullsecret1234")
        }
      }
    })
    error_message = "The pull Secret must be a docker config for the registry the key's credential is for, with the credential that came with the key."
  }

  assert {
    condition = (yamldecode(helm_release.byoc.values[0]).imageSource == { mode = "direct", pullSecretName = "restate-registry" }
    && !can(yamldecode(helm_release.byoc.values[0]).imageSource.registry))
    error_message = "The chart must be told to pull directly, with the Secret's name and no registry of its own, because it derives the registry."
  }

  assert {
    condition = yamldecode(helm_release.byoc.values[0]).restateOperator == {
      enabled             = true
      image               = { repository = "registry.dev.restate.cloud/restate-operator" }
      canaryImage         = "registry.dev.restate.cloud/alpine:3.21"
      gcpWorkloadIdentity = true
      imagePullSecrets    = [{ name = "restate-registry" }]
    }
    error_message = "restate-operator must pull from Restate's registry with the pull Secret, because a subchart cannot read the parent's values and the chart refuses other repositories."
  }

  assert {
    condition     = !can(yamldecode(helm_release.byoc.values[0]).regionInstallationOperator)
    error_message = "The chart refuses regionInstallationOperator.imagePullSecrets in direct mode, so the module must not set them."
  }

  assert {
    condition     = [for m in google_project_iam_member.nodes : m.role] == ["roles/container.defaultNodeServiceAccount"]
    error_message = "Direct mode needs no registry role on the nodes."
  }
}

run "pull_secret_can_be_named" {
  command = apply

  variables {
    image_pull_secret_name            = "acme-pull"
    restate_operator_canary_image_tag = "3.22"
  }

  assert {
    condition = (kubernetes_secret_v1.image_pull[0].metadata[0].name == "acme-pull"
      && yamldecode(helm_release.byoc.values[0]).imageSource.pullSecretName == "acme-pull"
      && yamldecode(helm_release.byoc.values[0]).restateOperator.imagePullSecrets == [{ name = "acme-pull" }]
    && yamldecode(helm_release.byoc.values[0]).restateOperator.canaryImage == "registry.dev.restate.cloud/alpine:3.22")
    error_message = "The Secret, the chart's pullSecretName and restate-operator's pull Secrets must all carry the name that was asked for."
  }
}

# The pull Secret's preconditions, which refuse a key that came without a credential and a credential for
# another registry than the one the chart pulls from, can be checked only in an apply with the key
# overridden, and `expect_failures` does not catch an apply-time failure in OpenTofu, nor does an
# override apply at plan time in Terraform 1.10, so no run asserts them.

run "refuses_a_registry_in_direct_mode" {
  command = plan

  variables {
    image_source_registry = "europe-docker.pkg.dev/acme/restate"
  }

  expect_failures = [var.image_source_registry]
}

run "refuses_an_unknown_mode" {
  command = plan

  variables {
    image_source_mode = "proxy"
  }

  expect_failures = [var.image_source_mode]
}

run "refuses_a_secret_the_chart_uses" {
  command = plan

  variables {
    image_pull_secret_name = "registration"
  }

  expect_failures = [var.image_pull_secret_name]
}
