# A region in a network of the customer's own, with the settings the module lets them change, and the
# inputs it refuses. Every provider is mocked.
mock_provider "google" {
  mock_data "google_client_config" {
    defaults = { access_token = "mock-token" }
  }
  mock_data "google_compute_zones" {
    defaults = { names = ["europe-west1-b", "europe-west1-c", "europe-west1-d"] }
  }
  mock_resource "google_container_cluster" {
    defaults = {
      endpoint    = "10.0.0.2"
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

mock_provider "restate" {
  mock_resource "restate_cloud_byok_registration_key" {
    defaults = {
      id         = "rgn_z9y8x7w6v5"
      region_id  = "rgn_z9y8x7w6v5"
      key        = "rrk_9z8y7x.6w5v4u"
      expires_at = "2026-11-18T16:00:00.000Z"
    }
  }
}

variables {
  project               = "my-restate-region"
  region                = "europe-west1"
  control_plane_url     = "https://api.restate.cloud"
  image_source_registry = "europe-docker.pkg.dev/acme/restate"
  byoc_chart = {
    chart             = "restate-byoc"
    repository        = "oci://example.test/charts"
    version           = "0.1.0"
    dependency_update = true
  }
}

run "network_of_your_own" {
  command = plan

  variables {
    name = "acme-eu"
    network = {
      name                = "projects/acme/global/networks/prod"
      subnetwork          = "projects/acme/regions/europe-west1/subnetworks/restate"
      pods_range_name     = "restate-pods"
      services_range_name = "restate-services"
    }
    enable_private_endpoint    = true
    master_authorized_networks = { "10.1.0.0/16" = "operators" }
    node_locations             = ["europe-west1-b", "europe-west1-c", "europe-west1-d"]
    deletion_protection        = false
    labels                     = { team = "platform" }
  }

  assert {
    condition = (length(google_compute_network.this) == 0
      && length(google_compute_subnetwork.this) == 0
      && length(google_compute_router.this) == 0
      && length(google_compute_router_nat.this) == 0
      && google_container_cluster.this.network == "projects/acme/global/networks/prod"
      && google_container_cluster.this.subnetwork == "projects/acme/regions/europe-west1/subnetworks/restate"
    && google_container_cluster.this.ip_allocation_policy[0].services_secondary_range_name == "restate-services")
    error_message = "With a network of its own the module must create none, and use the ranges it names."
  }

  assert {
    condition = (google_container_cluster.this.private_cluster_config[0].enable_private_endpoint
      && one([for c in google_container_cluster.this.master_authorized_networks_config[0].cidr_blocks : c.cidr_block]) == "10.1.0.0/16"
      && !google_container_cluster.this.deletion_protection
      && google_container_cluster.this.resource_labels == tomap({ team = "platform" })
      && google_container_node_pool.environments.node_locations == toset(["europe-west1-b", "europe-west1-c", "europe-west1-d"])
    && google_service_account.environments.account_id == "acme-eu-env")
    error_message = "The cluster must follow the private endpoint, the authorized networks, the protection, the labels and the zones, and name its accounts after the region."
  }

  assert {
    condition = (google_storage_bucket.snapshots.name == "acme-eu-snapshots-my-restate-region"
    && google_storage_bucket.snapshots.location == "EUROPE-WEST1")
    error_message = "The bucket must be named after the region and the project, and be in the cluster's region."
  }
}

run "chart_from_a_repository_with_overrides" {
  command = apply

  variables {
    staff_gate_enabled                = false
    hold                              = true
    restate_region_display_name       = "acme eu"
    deregister_region_on_destroy      = true
    node_locations                    = ["europe-west1-b", "europe-west1-c", "europe-west1-d", "europe-west1-f"]
    namespace                         = "restate"
    release_name                      = "restate-byoc"
    registration_secret_name          = "restate-registration"
    restate_operator_canary_image_tag = "3.22"
    chart_values                      = ["hold: false\nregionInstallationOperator:\n  imagePullSecrets: [pull]\n"]
    snapshot_bucket_name              = "acme-restate-snapshots"
  }

  assert {
    condition = (helm_release.byoc.chart == "restate-byoc"
      && helm_release.byoc.repository == "oci://example.test/charts"
      && helm_release.byoc.version == "0.1.0"
      && helm_release.byoc.dependency_update
      && helm_release.byoc.name == "restate-byoc"
      && helm_release.byoc.namespace == "restate"
      && length(helm_release.byoc.values) == 2
    && yamldecode(helm_release.byoc.values[1]).regionInstallationOperator.imagePullSecrets == ["pull"])
    error_message = "The chart must come from the repository at its version, and extra values must follow the module's own, so that they can override them."
  }

  assert {
    condition = (yamldecode(helm_release.byoc.values[0]).staffGate == { enabled = false }
      && yamldecode(helm_release.byoc.values[0]).hold == true
      && yamldecode(helm_release.byoc.values[0]).registration == { secretName = "restate-registration" }
      && yamldecode(helm_release.byoc.values[0]).environments.snapshotBucket == "acme-restate-snapshots"
    && yamldecode(helm_release.byoc.values[0]).restateOperator.canaryImage == "europe-docker.pkg.dev/acme/restate/alpine:3.22")
    error_message = "The staff gate, hold, Secret name, bucket and canary image must follow the variables."
  }

  assert {
    condition = (restate_cloud_byok_registration_key.this.region_display_name == "acme eu"
      && restate_cloud_byok_registration_key.this.deregister_region_on_destroy
      && kubernetes_secret_v1.registration.metadata[0].name == "restate-registration"
    && yamldecode(helm_release.byoc.values[0]).region.id == "rgn_z9y8x7w6v5")
    error_message = "The key must carry the display name and remove the region on destroy when asked to."
  }

  assert {
    condition     = yamldecode(helm_release.byoc.values[0]).loadBalancers == { tunnel = { zones = 4 } }
    error_message = "With node_locations the chart must create one tunnel Service for each listed zone, not for each zone of the region, which the mock reports as three."
  }
}

run "tunnel_zones_can_be_set" {
  command = apply

  variables {
    tunnel_zones   = 3
    node_locations = ["europe-west1-b", "europe-west1-c"]
  }

  assert {
    condition     = yamldecode(helm_release.byoc.values[0]).loadBalancers == { tunnel = { zones = 3 } }
    error_message = "tunnel_zones must make two node locations acceptable and override the count taken from them."
  }
}

run "refuses_two_node_locations_without_tunnel_zones" {
  command = plan

  variables {
    node_locations = ["europe-west1-b", "europe-west1-c"]
  }

  expect_failures = [var.node_locations]
}

run "caps_node_locations_at_nine_tunnels" {
  command = apply

  variables {
    tunnel_zones   = 9
    node_locations = ["z1", "z2", "z3", "z4", "z5", "z6", "z7", "z8", "z9", "z10"]
  }

  assert {
    condition     = yamldecode(helm_release.byoc.values[0]).loadBalancers == { tunnel = { zones = 9 } }
    error_message = "More than nine node locations need an explicit tunnel_zones, which the chart accepts up to nine."
  }
}

run "refuses_ten_node_locations_without_tunnel_zones" {
  command = plan

  variables {
    node_locations = ["z1", "z2", "z3", "z4", "z5", "z6", "z7", "z8", "z9", "z10"]
  }

  expect_failures = [var.node_locations]
}

run "refuses_tunnel_zones_outside_three_to_nine" {
  command = plan

  variables {
    tunnel_zones = 2
  }

  expect_failures = [var.tunnel_zones]
}

run "refuses_ten_tunnel_zones" {
  command = plan

  variables {
    tunnel_zones = 10
  }

  expect_failures = [var.tunnel_zones]
}

# A region with fewer than three available zones would give the chart a count it refuses.
run "refuses_a_region_with_two_zones" {
  command = plan

  override_data {
    target = data.google_compute_zones.available
    values = { names = ["europe-west1-b", "europe-west1-c"] }
  }

  expect_failures = [helm_release.byoc]
}

run "caps_a_region_with_many_zones_at_nine" {
  command = apply

  override_data {
    target = data.google_compute_zones.available
    values = { names = ["z1", "z2", "z3", "z4", "z5", "z6", "z7", "z8", "z9", "z10", "z11"] }
  }

  assert {
    condition     = yamldecode(helm_release.byoc.values[0]).loadBalancers == { tunnel = { zones = 9 } }
    error_message = "The zones of the region must be capped at the nine tunnels the chart accepts."
  }
}

run "environments_role_can_be_narrowed" {
  command = plan

  variables {
    config_connector_environment_role = "organizations/123/roles/environmentsIamPolicyAdmin"
    node_service_account_roles        = ["roles/container.defaultNodeServiceAccount", "roles/artifactregistry.reader"]
    config_connector_ready_delay      = "300s"
  }

  assert {
    condition = (google_service_account_iam_member.config_connector_environments.role == "organizations/123/roles/environmentsIamPolicyAdmin"
      && length(google_project_iam_member.nodes) == 2
    && time_sleep.config_connector_kind.create_duration == "300s")
    error_message = "The role on the environments' account, the node roles and the wait for Config Connector must follow the variables."
  }
}

run "refuses_a_zone_as_the_region" {
  command = plan

  variables {
    region = "us-central1-a"
  }

  expect_failures = [var.region]
}

run "refuses_a_control_plane_with_a_path" {
  command = plan

  variables {
    control_plane_url = "https://api.restate.cloud/cloud"
  }

  expect_failures = [var.control_plane_url]
}

run "refuses_a_registry_with_a_trailing_slash" {
  command = plan

  variables {
    image_source_registry = "europe-docker.pkg.dev/acme/restate/"
  }

  expect_failures = [var.image_source_registry]
}

run "refuses_a_name_that_overflows_a_service_account_id" {
  command = plan

  variables {
    name = "a-region-with-a-very-long-name"
  }

  expect_failures = [var.name]
}
