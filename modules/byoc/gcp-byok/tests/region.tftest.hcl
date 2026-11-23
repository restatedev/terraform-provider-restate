# A BYOK region with the module's own network, whose images come from the customer's mirror. Nothing here reaches Google Cloud, a cluster or
# Restate Cloud: every provider is mocked.
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
}

mock_provider "kubernetes" {}

mock_provider "helm" {}

mock_provider "time" {}

# What Restate Cloud answers for a new key, as the restate provider exports it.
mock_provider "restate" {
  mock_resource "restate_cloud_byok_registration_key" {
    defaults = {
      id         = "rgn_a1b2c3d4e5"
      region_id  = "rgn_a1b2c3d4e5"
      key        = "rrk_1a2b3c.4d5e6f"
      expires_at = "2026-11-18T16:00:00.000Z"
    }
  }
}

# The zones Google Cloud picks when node_locations is not set, in no particular order, because the
# provider reports a set. The module sorts them for the chart.
override_resource {
  target = google_container_cluster.this
  values = { node_locations = ["us-central1-f", "us-central1-a", "us-central1-c", "us-central1-b"] }
}

# Each account answers its own email, so that an assertion can tell them apart.
override_resource {
  target = google_service_account.environments
  values = {
    email  = "restate-env@my-restate-region.iam.gserviceaccount.com"
    name   = "projects/my-restate-region/serviceAccounts/restate-env@my-restate-region.iam.gserviceaccount.com"
    member = "serviceAccount:restate-env@my-restate-region.iam.gserviceaccount.com"
  }
}

override_resource {
  target = google_service_account.config_connector
  values = {
    email  = "restate-cnrm@my-restate-region.iam.gserviceaccount.com"
    name   = "projects/my-restate-region/serviceAccounts/restate-cnrm@my-restate-region.iam.gserviceaccount.com"
    member = "serviceAccount:restate-cnrm@my-restate-region.iam.gserviceaccount.com"
  }
}

override_resource {
  target = google_service_account.nodes
  values = {
    email  = "restate-nodes@my-restate-region.iam.gserviceaccount.com"
    name   = "projects/my-restate-region/serviceAccounts/restate-nodes@my-restate-region.iam.gserviceaccount.com"
    member = "serviceAccount:restate-nodes@my-restate-region.iam.gserviceaccount.com"
  }
}

variables {
  project               = "my-restate-region"
  region                = "us-central1"
  control_plane_url     = "https://api.dev.restate.cloud"
  image_source_mode     = "mirror"
  image_source_registry = "us-central1-docker.pkg.dev/my-restate-region/restate"
  byoc_chart            = { chart = "../../../../charts/byoc" }
}

run "cluster" {
  command = plan

  assert {
    condition = (google_container_cluster.this.rbac_binding_config[0].enable_insecure_binding_system_unauthenticated
    && !google_container_cluster.this.rbac_binding_config[0].enable_insecure_binding_system_authenticated)
    error_message = "The Concierge binds its login API to system:unauthenticated, so GKE must allow that group, and the binding to system:authenticated must stay refused."
  }

  assert {
    condition = (google_container_cluster.this.workload_identity_config[0].workload_pool == "my-restate-region.svc.id.goog"
    && google_container_cluster.this.addons_config[0].config_connector_config[0].enabled)
    error_message = "The cluster needs workload identity and the Config Connector add-on."
  }

  assert {
    condition = (google_container_cluster.this.location == "us-central1"
      && google_container_cluster.this.remove_default_node_pool
      && google_container_cluster.this.datapath_provider == "ADVANCED_DATAPATH"
      && google_container_cluster.this.deletion_protection
      && google_container_cluster.this.private_cluster_config[0].enable_private_nodes
      && !google_container_cluster.this.private_cluster_config[0].enable_private_endpoint
    && google_container_cluster.this.release_channel[0].channel == "REGULAR")
    error_message = "The cluster must be regional, keep network policies enforced, have private nodes and be protected from deletion."
  }

  assert {
    condition = (contains(keys(google_project_service.this), "iamcredentials.googleapis.com")
    && contains(keys(google_project_service.this), "cloudresourcemanager.googleapis.com"))
    error_message = "Workload identity exchanges tokens through the IAM Credentials API, and the IAM bindings and Config Connector use Cloud Resource Manager, so both must be enabled."
  }

  assert {
    condition     = length(google_container_cluster.this.master_authorized_networks_config) == 0
    error_message = "Without master_authorized_networks the cluster must not restrict the API server."
  }
}

run "network" {
  command = plan

  assert {
    condition = (length(google_compute_network.this) == 1
      && length(google_compute_router_nat.this) == 1
      && google_compute_subnetwork.this[0].private_ip_google_access
      && google_compute_subnetwork.this[0].ip_cidr_range == "10.10.0.0/20"
      && [for r in google_compute_subnetwork.this[0].secondary_ip_range : r.range_name] == ["pods", "services"]
    && google_container_cluster.this.ip_allocation_policy[0].cluster_secondary_range_name == "pods")
    error_message = "Without a network of its own, the module must create a network, a subnetwork with the pods and services ranges, and Cloud NAT for the private nodes."
  }
}

run "node_pools" {
  command = plan

  assert {
    condition = (one([for t in google_container_node_pool.environments.node_config[0].taint : t.key]) == "restate.dev/environments"
      && one([for t in google_container_node_pool.environments.node_config[0].taint : t.value]) == "true"
      && one([for t in google_container_node_pool.environments.node_config[0].taint : t.effect]) == "NO_SCHEDULE"
    && google_container_node_pool.environments.node_config[0].labels == tomap({ "restate.dev/environments" = "true" }))
    error_message = "The environments node pool must be tainted restate.dev/environments=true:NoSchedule and carry the same label."
  }

  assert {
    condition = (length(google_container_node_pool.system.node_config[0].taint) == 0
      && google_container_node_pool.system.node_config[0].workload_metadata_config[0].mode == "GKE_METADATA"
    && google_container_node_pool.environments.node_config[0].workload_metadata_config[0].mode == "GKE_METADATA")
    error_message = "The system pool must take the chart's own components, so it has no taint, and both pools must use workload identity."
  }

  assert {
    condition = (google_container_node_pool.system.autoscaling[0].max_node_count == 2
    && google_container_node_pool.environments.autoscaling[0].max_node_count == 3)
    error_message = "The node pools must autoscale between the counts the variables set."
  }
}

run "identities" {
  command = plan

  assert {
    condition = (google_service_account.environments.account_id == "restate-env"
      && google_service_account.config_connector.account_id == "restate-cnrm"
    && google_service_account.nodes.account_id == "restate-nodes")
    error_message = "The three service accounts must be named after the region."
  }

  assert {
    condition = (google_storage_bucket_iam_member.environments.bucket == "restate-snapshots-my-restate-region"
      && google_storage_bucket_iam_member.environments.role == "roles/storage.objectAdmin"
      && google_storage_bucket.snapshots.uniform_bucket_level_access
      && google_storage_bucket.snapshots.public_access_prevention == "enforced"
      && google_storage_bucket.snapshots.location == "US-CENTRAL1"
    && !google_storage_bucket.snapshots.force_destroy)
    error_message = "The environments must hold objectAdmin on the snapshot bucket only, and the bucket must be private and refuse a forced destroy."
  }

  assert {
    condition = (google_service_account_iam_member.config_connector_environments.role == "roles/iam.serviceAccountAdmin"
      && google_service_account_iam_member.config_connector_workload_identity.member == "serviceAccount:my-restate-region.svc.id.goog[cnrm-system/cnrm-controller-manager]"
    && google_service_account_iam_member.config_connector_workload_identity.role == "roles/iam.workloadIdentityUser")
    error_message = "Config Connector must hold its role on the environments' service account, and the add-on's controller must be able to act as Config Connector's account."
  }

  assert {
    condition     = [for m in google_project_iam_member.nodes : m.role] == ["roles/container.defaultNodeServiceAccount"]
    error_message = "The nodes must hold only the default node role in the project."
  }
}

run "registration" {
  command = plan

  assert {
    condition = (restate_cloud_byok_registration_key.this.cloud == "gcp"
      && restate_cloud_byok_registration_key.this.provider_region == "us-central1"
      && restate_cloud_byok_registration_key.this.region_display_name == null
    && !restate_cloud_byok_registration_key.this.deregister_region_on_destroy)
    error_message = "The key must be for the cluster's region, and a destroy must leave a registered region in place unless asked to remove it."
  }

  assert {
    condition = (kubernetes_namespace_v1.release.metadata[0].name == "restate-system"
      && kubernetes_secret_v1.registration.metadata[0].name == "registration"
      && kubernetes_secret_v1.registration.metadata[0].namespace == "restate-system"
    && nonsensitive(toset(keys(kubernetes_secret_v1.registration.data))) == toset(["key"]))
    error_message = "The registration Secret must carry one entry, `key`, in the chart's namespace, under the name the chart reads."
  }
}

run "chart_values" {
  command = apply

  assert {
    condition     = nonsensitive(kubernetes_secret_v1.registration.data["key"]) == "rrk_1a2b3c.4d5e6f"
    error_message = "The registration Secret must hold the key Restate Cloud issued."
  }

  assert {
    condition = (helm_release.byoc.name == "byoc"
      && helm_release.byoc.namespace == "restate-system"
      && helm_release.byoc.chart == "../../../../charts/byoc"
      && !helm_release.byoc.wait
    && length(helm_release.byoc.values) == 1)
    error_message = "The chart's release must not wait, because its pods become ready only after the region has registered, and must take the module's values."
  }

  assert {
    condition = (!can(yamldecode(helm_release.byoc.values[0]).loadBalancers)
    && helm_release.config_connector.wait)
    error_message = "The chart must no longer be given loadBalancers.tunnel.zones, which region.availabilityZones replaced, and Config Connector's object must be waited for."
  }

  assert {
    condition = (yamldecode(helm_release.byoc.values[0]).region == {
      id                = "rgn_a1b2c3d4e5"
      cloud             = "gcp"
      providerRegion    = "us-central1"
      availabilityZones = ["us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"]
      }
      && yamldecode(helm_release.byoc.values[0]).controlPlane == { url = "https://api.dev.restate.cloud" }
      && yamldecode(helm_release.byoc.values[0]).imageSource == { mode = "mirror", registry = "us-central1-docker.pkg.dev/my-restate-region/restate" }
    && yamldecode(helm_release.byoc.values[0]).registration == { secretName = "registration" })
    error_message = "The chart must be told the region the key reserved, its cloud and provider region, the sorted zones of the cluster, the control plane and the image source."
  }

  assert {
    condition = (length(kubernetes_secret_v1.image_pull) == 0
      && !can(yamldecode(helm_release.byoc.values[0]).restateOperator.imagePullSecrets)
    && output.image_registry == "us-central1-docker.pkg.dev/my-restate-region/restate")
    error_message = "Mirror mode must create no pull Secret and give restate-operator no pull Secrets, because the customer's registry has its own credentials."
  }

  assert {
    condition = (yamldecode(helm_release.byoc.values[0]).environments == {
      gcpServiceAccount = "restate-env@my-restate-region.iam.gserviceaccount.com"
      snapshotBucket    = "restate-snapshots-my-restate-region"
      }
      && yamldecode(helm_release.byoc.values[0]).scheduling == {
        nodeSelector = { "restate.dev/environments" = "true" }
        tolerations  = [{ key = "restate.dev/environments", operator = "Equal", value = "true", effect = "NoSchedule" }]
    })
    error_message = "The chart must be given the environments' service account, the bucket and the scheduling of the environments node pool."
  }

  assert {
    condition = (yamldecode(helm_release.byoc.values[0]).staffGate == { enabled = true }
      && yamldecode(helm_release.byoc.values[0]).hold == false
      && yamldecode(helm_release.byoc.values[0]).restateOperator == {
        enabled             = true
        image               = { repository = "us-central1-docker.pkg.dev/my-restate-region/restate/restate-operator" }
        canaryImage         = "us-central1-docker.pkg.dev/my-restate-region/restate/alpine:3.21"
        gcpWorkloadIdentity = true
    })
    error_message = "The chart's staff gate, hold and restate-operator values must follow the variables, and restate-operator's images must come from the image source."
  }

  assert {
    condition = (helm_release.config_connector.namespace == "kube-system"
    && yamldecode(helm_release.config_connector.values[0]).googleServiceAccount == "restate-cnrm@my-restate-region.iam.gserviceaccount.com")
    error_message = "Config Connector must be pointed at its own service account."
  }

  assert {
    condition = (output.region_id == "rgn_a1b2c3d4e5"
      && output.snapshot_bucket == "restate-snapshots-my-restate-region"
      && output.scheduling.nodeSelector["restate.dev/environments"] == "true"
    && output.registration_key_expires_at == "2026-11-18T16:00:00.000Z")
    error_message = "The outputs must carry the region the key reserved, the bucket and the scheduling."
  }
}
