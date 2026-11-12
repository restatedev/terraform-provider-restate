# A region grant for the AWS account, and the regions built with it, in one apply. Run with AWS
# credentials for the account. The restate provider takes its credential from the environment:
# RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY.
terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

provider "restate" {}

module "restate_byoc" {
  source = "../../../aws"

  region_grant = {
    # ORGANIZATION lets every account of the organization build regions with the grant. The
    # provider then needs an organization administrator's token, or an organization API key with
    # role rst:role::OrganizationRegionGrantAccess.
    owner = var.owner
    regions = {
      prod-eu = { deletion_protection = true }
    }
  }
  oidc_provider_arn = var.oidc_provider_arn
}

variable "owner" {
  type    = string
  default = "ACCOUNT"
}

# The arn output of examples/aws/oidc-provider.
variable "oidc_provider_arn" {
  type = string
}

output "grant_id" {
  value = module.restate_byoc.grant_id
}

output "region_ids" {
  value = module.restate_byoc.region_ids
}
