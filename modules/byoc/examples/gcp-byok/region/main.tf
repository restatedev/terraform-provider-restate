# A BYOK region: a GKE cluster that this configuration builds, registered with Restate Cloud, with
# the BYOC chart installed. Run with Google Cloud credentials for the project. The restate provider
# takes its credential from the environment: RESTATE_CLOUD_ACCOUNT_ID and
# RESTATE_CLOUD_MANAGEMENT_API_KEY.
terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

provider "restate" {}

module "restate_byok" {
  source = "../../../gcp-byok"

  project = var.project
  region  = var.region

  control_plane_url     = var.control_plane_url
  image_source_registry = var.image_source_registry
  byoc_chart            = var.byoc_chart

  # Only the networks that run Terraform need to reach the API server.
  master_authorized_networks = var.master_authorized_networks
}

variable "project" {
  type = string
}

variable "region" {
  type    = string
  default = "us-central1"
}

variable "control_plane_url" {
  description = "The https://api.<domain> address of Restate's control plane, which Restate Cloud provides."
  type        = string
}

variable "image_source_registry" {
  description = "Where Restate's images are mirrored, for example us-central1-docker.pkg.dev/my-project/restate."
  type        = string
}

variable "byoc_chart" {
  description = "Where the BYOC chart comes from. For a checkout, set chart to the path of charts/byoc and dependency_update to true."
  type = object({
    chart             = string
    repository        = optional(string)
    version           = optional(string)
    dependency_update = optional(bool, false)
  })
}

variable "master_authorized_networks" {
  type    = map(string)
  default = {}
}

output "region_id" {
  value = module.restate_byok.region_id
}
