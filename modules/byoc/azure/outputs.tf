output "subscription_id" {
  description = "The subscription the region is built in."
  value       = var.subscription_id
}

output "grant" {
  description = "What Restate Cloud needs to use the grant. Without `restate`, give it to Restate Cloud unchanged."
  value = merge(
    {
      tenantId = data.azuread_client_config.current.tenant_id
      clientId = azuread_application.grant.client_id
    },
    # A region grant has no resource group, so ConfirmRegionGrant takes only the first two.
    local.grant_mode ? {} : { resourceGroup = azurerm_resource_group.region[0].name },
  )
}

output "region_id" {
  description = "With `restate`: the region Restate Cloud created and is building."
  value       = var.restate != null ? restapi_object.reservation[0].id : null
}

output "grant_id" {
  description = "With `region_grant`: the confirmed region grant. Administrators pass it as grant_id to restate_cloud_byoc_region, which then waits for the confirmation and is destroyed before it."
  value       = one(restate_cloud_region_grant_confirmation.this[*].grant_id)
}

output "resource_prefix" {
  description = "With `region_grant`: the prefix of every resource name a region of the grant has in the subscription."
  value       = local.grant_mode ? local.region_grant_inputs.resourcePrefix : null
}

output "status" {
  description = "With `region_grant`: the grant's status, READY once it is confirmed."
  value       = local.grant_mode ? restate_cloud_region_grant_confirmation.this[0].status : null
}

output "region_ids" {
  description = "With `region_grant`: the ids of the regions in region_grant.regions, by name."
  value       = { for name, region in restate_cloud_byoc_region.this : name => region.id }
}

output "restate_api_key" {
  description = "With `restate_access_token`: the Management API key the module created, for managing environments in the region."
  value       = local.created_api_key
  sensitive   = true
}
