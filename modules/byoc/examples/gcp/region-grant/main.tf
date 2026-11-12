# A region grant for a project dedicated to its regions, and the regions built with it, in one
# apply. Run with Google Cloud credentials for the project. The restate provider takes its
# credential from the environment: RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY.
terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

provider "restate" {}

module "restate_byoc" {
  source = "../../../gcp"

  project = var.project
  region_grant = {
    # ORGANIZATION lets every account of the organization build regions with the grant. The
    # provider then needs an organization administrator's token, or an organization API key with
    # role rst:role::OrganizationRegionGrantAccess.
    owner = var.owner
    regions = {
      prod-eu = { cloud_region = "europe-west1", deletion_protection = true }
    }
  }
}

variable "project" {
  type = string
}

variable "owner" {
  type    = string
  default = "ACCOUNT"
}

output "grant_id" {
  value = module.restate_byoc.grant_id
}

output "region_ids" {
  value = module.restate_byoc.region_ids
}
