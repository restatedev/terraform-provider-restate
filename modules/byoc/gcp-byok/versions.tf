terraform {
  required_version = ">= 1.10"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.1"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.12"
    }
    restate = {
      source = "restatedev/restate"
    }
  }
}

# Credentials come from Application Default Credentials, with rights to create the cluster, the
# service accounts and the bucket, and to change the project's IAM.
provider "google" {
  project = var.project
  region  = var.region
}

data "google_client_config" "current" {}

# The cluster does not exist when Terraform plans the first apply, so both providers read their
# connection from the cluster's own attributes. Terraform needs a network path to the API server for
# the Secret and the chart, which master_authorized_networks and enable_private_endpoint decide.
provider "kubernetes" {
  host                   = "https://${google_container_cluster.this.endpoint}"
  token                  = data.google_client_config.current.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.this.master_auth[0].cluster_ca_certificate)
}

provider "helm" {
  kubernetes = {
    host                   = "https://${google_container_cluster.this.endpoint}"
    token                  = data.google_client_config.current.access_token
    cluster_ca_certificate = base64decode(google_container_cluster.this.master_auth[0].cluster_ca_certificate)
  }
}
