# The GKE cluster of a BYOK region: a cluster the customer builds, with the pieces the BYOC chart
# expects of it. identity.tf creates the identities and the bucket, and restate.tf registers the
# region and installs the chart.
#
# The cluster follows the one Managed BYOC builds for a Google Cloud region (byoc-infra's
# cluster.yaml.tmpl in restatedev/restate-cloud), where it differs in what the customer owns. There is
# no node auto-provisioning: two node pools of a fixed shape hold the chart's components and the
# environments, and the customer resizes them.

locals {
  environments_label_key   = "restate.dev/environments"
  environments_label_value = "true"

  create_network = var.network == null
  network = local.create_network ? {
    name                = google_compute_network.this[0].id
    subnetwork          = google_compute_subnetwork.this[0].id
    pods_range_name     = "pods"
    services_range_name = "services"
  } : var.network

  # iamcredentials is what workload identity exchanges tokens through, and cloudresourcemanager is what
  # the project's IAM bindings and Config Connector call.
  apis = [
    "cloudresourcemanager.googleapis.com",
    "compute.googleapis.com",
    "container.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "storage.googleapis.com",
  ]

  # The zones the cluster runs in, which the chart takes as region.availabilityZones. The provider
  # reports them as a set, which has no order of its own, so they are sorted to give the chart the same
  # list on every apply, because the chart's list is ordered. Google Cloud picks them when node_locations
  # is not set, so they are unknown until the cluster exists.
  availability_zones = sort(tolist(google_container_cluster.this.node_locations))
}

resource "google_project_service" "this" {
  for_each           = var.enable_apis ? toset(local.apis) : toset([])
  project            = var.project
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "this" {
  count                   = local.create_network ? 1 : 0
  project                 = var.project
  name                    = var.name
  auto_create_subnetworks = false

  depends_on = [google_project_service.this]
}

resource "google_compute_subnetwork" "this" {
  count                    = local.create_network ? 1 : 0
  project                  = var.project
  name                     = var.name
  region                   = var.region
  network                  = google_compute_network.this[0].id
  ip_cidr_range            = var.subnet_cidr
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = var.pods_cidr
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = var.services_cidr
  }
}

# Nodes without external addresses still pull Restate's images and reach the control plane, so they
# leave through Cloud NAT.
resource "google_compute_router" "this" {
  count   = local.create_network && var.enable_private_nodes ? 1 : 0
  project = var.project
  name    = var.name
  region  = var.region
  network = google_compute_network.this[0].id
}

resource "google_compute_router_nat" "this" {
  count                              = local.create_network && var.enable_private_nodes ? 1 : 0
  project                            = var.project
  name                               = var.name
  region                             = var.region
  router                             = google_compute_router.this[0].name
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

resource "google_container_cluster" "this" {
  project  = var.project
  name     = var.name
  location = var.region

  # GKE insists on a default pool, so it is created and removed, which leaves the two pools below as
  # the only ones.
  remove_default_node_pool = true
  initial_node_count       = 1

  # A cluster whose network policies are not enforced would leave the chart's environments open to each
  # other, so the data plane is Dataplane V2, which enforces them.
  datapath_provider = "ADVANCED_DATAPATH"
  network           = local.network.name
  subnetwork        = local.network.subnetwork

  min_master_version    = var.kubernetes_version
  node_locations        = var.node_locations
  deletion_protection   = var.deletion_protection
  enable_shielded_nodes = true
  resource_labels       = var.labels

  release_channel {
    channel = var.release_channel
  }

  ip_allocation_policy {
    cluster_secondary_range_name  = local.network.pods_range_name
    services_secondary_range_name = local.network.services_range_name
  }

  # Everything in the cluster that calls Google acts as a Google service account through workload
  # identity, so no key is distributed.
  workload_identity_config {
    workload_pool = "${var.project}.svc.id.goog"
  }

  # Config Connector is the GKE add-on. It applies the IAM binding of each environment's ServiceAccount
  # to the environments' Google service account, and runs nothing until identity.tf points it at the
  # service account it acts as.
  addons_config {
    config_connector_config {
      enabled = true
    }
  }

  # The Concierge binds its login API to system:unauthenticated, because the impersonation proxy
  # authorizes the anonymous TokenCredentialRequest with RBAC, and GKE refuses a binding to that group
  # unless this is on. The binding to system:authenticated stays refused: nothing of Restate's needs
  # it, and it would widen every signed-in identity.
  rbac_binding_config {
    enable_insecure_binding_system_unauthenticated = true
    enable_insecure_binding_system_authenticated   = false
  }

  private_cluster_config {
    enable_private_nodes    = var.enable_private_nodes
    enable_private_endpoint = var.enable_private_endpoint
    master_ipv4_cidr_block  = var.enable_private_nodes ? var.master_ipv4_cidr_block : null
  }

  dynamic "master_authorized_networks_config" {
    for_each = length(var.master_authorized_networks) > 0 ? [1] : []
    content {
      dynamic "cidr_blocks" {
        for_each = var.master_authorized_networks
        content {
          cidr_block   = cidr_blocks.key
          display_name = cidr_blocks.value
        }
      }
    }
  }

  master_auth {
    client_certificate_config {
      issue_client_certificate = false
    }
  }

  depends_on = [google_project_service.this]

  lifecycle {
    # The default pool is removed after the cluster is created, so its size is not the configuration's.
    ignore_changes = [initial_node_count]
  }
}

resource "google_container_node_pool" "system" {
  project        = var.project
  name           = "system"
  cluster        = google_container_cluster.this.name
  location       = var.region
  node_locations = var.node_locations

  initial_node_count = var.system_node_pool.min_node_count

  autoscaling {
    min_node_count = var.system_node_pool.min_node_count
    max_node_count = var.system_node_pool.max_node_count
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  node_config {
    machine_type    = var.system_node_pool.machine_type
    disk_size_gb    = var.system_node_pool.disk_size_gb
    disk_type       = var.system_node_pool.disk_type
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    resource_labels = var.labels

    # Workload identity is on for the cluster, and has to be on for each pool too, or the nodes stay on
    # the legacy metadata endpoint.
    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }

  lifecycle {
    ignore_changes = [initial_node_count]
  }
}

# The chart's scheduling (restate.tf) lets environment pods use this label and this toleration and no
# others, so the environments run on these nodes and nothing else of the chart's does. A pod that
# does not tolerate the taint cannot land here.
resource "google_container_node_pool" "environments" {
  project        = var.project
  name           = "environments"
  cluster        = google_container_cluster.this.name
  location       = var.region
  node_locations = var.node_locations

  initial_node_count = var.environments_node_pool.min_node_count

  autoscaling {
    min_node_count = var.environments_node_pool.min_node_count
    max_node_count = var.environments_node_pool.max_node_count
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  node_config {
    machine_type    = var.environments_node_pool.machine_type
    disk_size_gb    = var.environments_node_pool.disk_size_gb
    disk_type       = var.environments_node_pool.disk_type
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    resource_labels = var.labels
    labels          = { (local.environments_label_key) = local.environments_label_value }

    taint {
      key    = local.environments_label_key
      value  = local.environments_label_value
      effect = "NO_SCHEDULE"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }

  lifecycle {
    ignore_changes = [initial_node_count]
  }
}
