variable "project" {
  description = "The project the cluster, the bucket and the service accounts are created in. Use a project of its own for the region: GKE gives the same workload identity to a namespace and a ServiceAccount in every cluster of a project, so another cluster there must keep out of the `env-*` namespaces."
  type        = string
}

variable "region" {
  description = "The Google Cloud region of the cluster and the bucket, for example `us-central1`. The cluster is regional, and Restate registers it under this name."
  type        = string

  validation {
    condition     = can(regex("^[a-z]+-[a-z]+[0-9]+$", var.region))
    error_message = "region must be a Google Cloud region such as us-central1, not a zone."
  }
}

variable "name" {
  description = "The name of the cluster, and the prefix of the service accounts and the bucket. It must differ between regions in the same project."
  type        = string
  default     = "restate"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,18}[a-z0-9]$", var.name))
    error_message = "name must be 2 to 20 lower-case letters, digits and hyphens, start with a letter and not end with a hyphen, so that the service account IDs fit in 30 characters."
  }
}

variable "labels" {
  description = "Google Cloud labels for the cluster, its nodes and the bucket."
  type        = map(string)
  default     = {}
}

variable "enable_apis" {
  description = "Enable the Google Cloud APIs the module uses. They stay enabled after a destroy. Turn it off when the project's APIs are managed elsewhere."
  type        = bool
  default     = true
}

variable "restate_region_display_name" {
  description = "The name the Restate Cloud console shows for the region. Restate chooses one when it is unset. The API takes the name only when it creates the registration key and has no operation that changes it afterwards, so changing this replaces the key. Set it before the first apply."
  type        = string
  default     = null
}

variable "deregister_region_on_destroy" {
  description = <<-EOT
    Remove the region at Restate Cloud when the registration key is destroyed, which a destroy of
    the module does after it has removed the chart and before it removes the cluster. Restate refuses
    while the region hosts environments, and the destroy then stops with the cluster still in place.
    The default is false, which leaves the region registered, so that a destroy of the module or a
    replacement of the key never removes a running region by surprise. With it on, changing the key's
    settings replaces the key and also removes the region the old key registered. Remove a region you
    leave registered in the console, or with `DeleteByokRegion`.
  EOT
  type        = bool
  default     = false
}

variable "control_plane_url" {
  description = "The `https://api.<domain>` address of Restate's control plane that the region registers with, with no port or path. Restate Cloud provides it."
  type        = string

  validation {
    condition     = can(regex("^https://api[.][a-z0-9]([a-z0-9.-]*[a-z0-9])?$", var.control_plane_url))
    error_message = "control_plane_url must be https://api.<domain> with no port or path, such as https://api.dev.restate.cloud."
  }
}

variable "image_source_registry" {
  description = "The registry and path that every Restate image in the cluster comes from, such as `europe-docker.pkg.dev/acme/restate`, with no trailing slash. Mirror Restate's images there. The cluster's nodes must be able to pull from it: add the reader role to `node_service_account_roles` for an Artifact Registry in your project."
  type        = string

  validation {
    condition     = length(var.image_source_registry) > 0 && !endswith(var.image_source_registry, "/")
    error_message = "image_source_registry must be a registry and path with no trailing slash."
  }
}

variable "byoc_chart" {
  description = <<-EOT
    Where the BYOC chart comes from. `chart` is a chart name with `repository`, or a path to a chart
    directory without it, such as the `charts/byoc` directory of a checkout. A local chart also needs
    `dependency_update = true` unless its dependencies are already built. `version` pins a chart from
    a repository.
  EOT
  type = object({
    chart             = string
    repository        = optional(string)
    version           = optional(string)
    dependency_update = optional(bool, false)
  })
}

variable "namespace" {
  description = "The namespace of the chart's release, which holds the registration Secret. The module creates it."
  type        = string
  default     = "restate-system"
}

variable "release_name" {
  description = "The name of the chart's Helm release."
  type        = string
  default     = "byoc"
}

variable "registration_secret_name" {
  description = "The name of the Secret that holds the registration key, which the chart reads as `registration.secretName`."
  type        = string
  default     = "registration"
}

variable "staff_gate_enabled" {
  description = "Let Restate staff reach the environment namespaces of the cluster, and nothing else in it, which the chart's `staffGate.enabled` turns on. Staff sign in through the Concierge with short-lived tokens."
  type        = bool
  default     = true
}

variable "hold" {
  description = "Keep the region at its applied release, which the chart's `hold` sets. The control plane cannot clear it."
  type        = bool
  default     = false
}

variable "restate_operator_canary_image_tag" {
  description = "The tag of `<image_source_registry>/alpine`, which restate-operator runs in environment namespaces to probe a cluster. The chart refuses any other repository. The default is the tag of the chart's own test values, so check it against the chart's release notes."
  type        = string
  default     = "3.21"
}

variable "chart_values" {
  description = "More values for the chart, as YAML documents. Helm merges them over the module's values in order, so they can override any of them."
  type        = list(string)
  default     = []
}

variable "helm_timeout_seconds" {
  description = "How long Terraform waits for Config Connector's object to be applied. The module does not wait for the BYOC chart, whose pods become ready only after the region has registered."
  type        = number
  default     = 900
}

variable "release_channel" {
  description = "The GKE release channel. The chart needs Kubernetes 1.30 or later."
  type        = string
  default     = "REGULAR"

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE"], var.release_channel)
    error_message = "release_channel must be RAPID, REGULAR or STABLE."
  }
}

variable "kubernetes_version" {
  description = "The minimum Kubernetes version of the control plane, for example `1.33`. The release channel chooses one when it is unset. The chart needs 1.30 or later."
  type        = string
  default     = null
}

variable "deletion_protection" {
  description = "GKE's own protection against deleting the cluster. Set it to false and apply before a destroy."
  type        = bool
  default     = true
}

variable "network" {
  description = <<-EOT
    A subnetwork of your own, with the names of its secondary ranges for pods and services. The module
    then creates no network, and the nodes need a route to the internet if `enable_private_nodes` is
    set, for example through Cloud NAT. Unset, the module creates a network, a subnetwork with the
    ranges below and, with private nodes, a router and Cloud NAT.
  EOT
  type = object({
    name                = string
    subnetwork          = string
    pods_range_name     = string
    services_range_name = string
  })
  default = null
}

variable "subnet_cidr" {
  description = "The primary range of the subnetwork the module creates."
  type        = string
  default     = "10.10.0.0/20"
}

variable "pods_cidr" {
  description = "The secondary range for pods of the subnetwork the module creates."
  type        = string
  default     = "10.20.0.0/14"
}

variable "services_cidr" {
  description = "The secondary range for services of the subnetwork the module creates."
  type        = string
  default     = "10.30.0.0/20"
}

variable "enable_private_nodes" {
  description = "Give the nodes no external addresses."
  type        = bool
  default     = true
}

variable "enable_private_endpoint" {
  description = "Give the API server only an internal address. Terraform then needs a route into the network."
  type        = bool
  default     = false
}

variable "master_ipv4_cidr_block" {
  description = "The /28 range of the private API server's peering, when `enable_private_nodes` is set."
  type        = string
  default     = "172.16.0.32/28"
}

variable "master_authorized_networks" {
  description = "The networks that may reach the API server, by CIDR block with a display name. Unset, any address may try, and sign-in still decides. Restate reaches the cluster through its Concierge and needs no entry here."
  type        = map(string)
  default     = {}
}

variable "node_locations" {
  description = "The zones of the node pools, for example `[\"us-central1-a\", \"us-central1-b\", \"us-central1-c\"]`. The default is every zone of the region that Google Cloud picks for the cluster. The module passes the zones of the cluster, sorted by name, to the chart as `region.availabilityZones`, and the chart creates one tunnel Service for each zone. The chart accepts 3 to 9 distinct zones, so give at least three, and no more than nine."
  type        = list(string)
  default     = null

  validation {
    condition     = var.node_locations == null ? true : (length(var.node_locations) >= 3 && length(var.node_locations) <= 9)
    error_message = "node_locations must list 3 to 9 zones, which is the range of availability zones that the BYOC chart accepts."
  }

  validation {
    condition     = var.node_locations == null ? true : length(distinct(var.node_locations)) == length(var.node_locations)
    error_message = "node_locations must not repeat a zone, because the BYOC chart needs distinct availability zones."
  }
}

variable "system_node_pool" {
  description = "The node pool for the chart's own components, the cloud operator, the ingress and the tunnel. The node counts are per zone."
  type = object({
    machine_type   = optional(string, "e2-standard-4")
    min_node_count = optional(number, 1)
    max_node_count = optional(number, 2)
    disk_size_gb   = optional(number, 100)
    disk_type      = optional(string, "pd-balanced")
  })
  default = {}
}

variable "environments_node_pool" {
  description = "The node pool for environments, tainted `restate.dev/environments=true:NoSchedule` and labelled `restate.dev/environments=true`. The node counts are per zone."
  type = object({
    machine_type   = optional(string, "e2-standard-8")
    min_node_count = optional(number, 1)
    max_node_count = optional(number, 3)
    disk_size_gb   = optional(number, 100)
    disk_type      = optional(string, "pd-balanced")
  })
  default = {}
}

variable "node_service_account_roles" {
  description = "The project roles of the service account the nodes run as. The default is what GKE nodes need. Add the reader role of the registry in `image_source_registry`, for example `roles/artifactregistry.reader`, when it is in your project."
  type        = list(string)
  default     = ["roles/container.defaultNodeServiceAccount"]
}

variable "snapshot_bucket_name" {
  description = "The name of the bucket for environments' snapshots. The default is `<name>-snapshots-<project>`."
  type        = string
  default     = null
}

variable "snapshot_bucket_force_destroy" {
  description = "Delete the bucket with the snapshots in it on a destroy. Off, a destroy fails while the bucket holds objects."
  type        = bool
  default     = false
}

variable "config_connector_environment_role" {
  description = "The role Config Connector's service account holds on the environments' service account, and on nothing else in the project. The default is the role the Managed BYOC regions grant. A custom role with `iam.serviceAccounts.getIamPolicy` and `iam.serviceAccounts.setIamPolicy` can replace it once you have tested that Config Connector works with it."
  type        = string
  default     = "roles/iam.serviceAccountAdmin"
}

variable "config_connector_ready_delay" {
  description = "How long to wait after the node pools exist before the module applies the `ConfigConnector` object. The GKE add-on installs that kind from a pod, so it appears some time after the first node. Raise it when the first apply fails with a missing kind."
  type        = string
  default     = "120s"
}
