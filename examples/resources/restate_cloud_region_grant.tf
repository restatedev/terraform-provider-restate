# Restate's half of a Managed BYOC grant for one AWS account. Its outputs are the inputs of the grant
# module in modules/byoc of this repository, which creates the cloud half: an IAM role that trusts
# Restate's issuer for this grant's subject, and only reaches names that start with resource_prefix.
resource "restate_cloud_region_grant" "aws" {
  cloud            = "aws"
  cloud_account_id = "123456789012"

  # ORGANIZATION lets every account of the organization build regions with the grant. It needs the
  # organization's administrator, or an organization API key with role
  # rst:role::OrganizationRegionGrantAccess, as the provider's credential.
  owner = "ACCOUNT"
}

output "grant_module_inputs" {
  value = {
    restate_issuer  = restate_cloud_region_grant.aws.restate_issuer
    grant_subject   = restate_cloud_region_grant.aws.grant_subject
    token_audience  = restate_cloud_region_grant.aws.token_audience
    resource_prefix = restate_cloud_region_grant.aws.resource_prefix
  }
}
