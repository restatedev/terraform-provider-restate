# A region grant for a subscription dedicated to its regions, and the regions built with it, in one
# apply. Run with Azure credentials for the subscription. The restate provider takes its credential
# from the environment: RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY.
terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

provider "restate" {}

module "restate_byoc" {
  source = "../../../azure"

  subscription_id = var.subscription_id
  region_grant = {
    # ORGANIZATION lets every account of the organization build regions with the grant. The
    # provider then needs an organization administrator's token, or an organization API key with
    # role rst:role::OrganizationRegionGrantAccess.
    owner = var.owner
    regions = {
      prod-eu = { cloud_region = "swedencentral", deletion_protection = true }
    }
  }
}

variable "subscription_id" {
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
