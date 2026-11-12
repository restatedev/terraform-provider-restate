terraform {
  required_version = ">= 1.10"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
    restapi = {
      source  = "Mastercard/restapi"
      version = "3.0.0"
    }
    restate = {
      source = "restatedev/restate"
    }
  }
}

# Credentials come from Application Default Credentials, with rights to change the project's IAM.
provider "google" {
  project = var.project
}
