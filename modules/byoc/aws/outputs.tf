output "account_id" {
  description = "The AWS account the region is built in."
  value       = local.account
}

output "grant" {
  description = "What Restate Cloud needs to use the grant: the role and the boundary. Without `restate`, give it to SetRegionGrant unchanged."
  value = {
    roleArn             = aws_iam_role.grant.arn
    permissionsBoundary = aws_iam_policy.boundary.arn
  }
}

output "external_id" {
  description = "The external id the role's trust policy requires. Null for a region that signs in with its own identity."
  value       = local.external_id
}

output "oidc_provider_arn" {
  description = "The IAM OIDC identity provider for Restate's issuer, which another region's grant in this account can pass as oidc_provider_arn. With `region_grant`, the one that was passed in. Null for a region that signs in through the composition cluster."
  value       = local.oidc_provider_arn
}

output "region_id" {
  description = "With `restate`: the region Restate Cloud created and is building."
  value       = var.restate != null ? restapi_object.reservation[0].id : null
}

output "restate_api_key" {
  description = "With `restate_access_token`: the Management API key the module created, for managing environments in the region."
  value       = local.created_api_key
  sensitive   = true
}

output "grant_id" {
  description = "With `region_grant`: the confirmed region grant. Administrators pass it as grant_id to restate_cloud_byoc_region, which then waits for the confirmation and is destroyed before it."
  value       = one(restate_cloud_region_grant_confirmation.this[*].grant_id)
}

output "resource_prefix" {
  description = "With `region_grant`: the prefix of every resource name a region of the grant has in this account."
  value       = local.region_grant_mode ? local.region_grant_inputs.resourcePrefix : null
}

output "status" {
  description = "With `region_grant`: the grant's status, READY once it is confirmed."
  value       = local.region_grant_mode ? restate_cloud_region_grant_confirmation.this[0].status : null
}

output "region_ids" {
  description = "With `region_grant`: the ids of the regions in region_grant.regions, by name."
  value       = { for name, region in restate_cloud_byoc_region.this : name => region.id }
}
