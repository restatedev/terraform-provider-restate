# The identity Restate Cloud's composition cluster uses to build a BYOC region in this
# subscription (credentials.source Grant), through workload identity federation. No secret exists.
#
# A region signs in in one of two ways, which Restate Cloud chooses when it creates the region:
#
# - With its own identity: a token from Restate's issuer whose subject names the region. One
#   federated credential accepts that issuer and subject, and nothing else.
# - Through the composition cluster: provider-azure presents a projected Kubernetes token with the
#   audience api://AzureADTokenExchange. Each federated credential accepts it only from the
#   composition cluster's issuer and only for one provider's ServiceAccount, and the resource
#   group's region tag binds the grant to the region.
#
# The identity is Contributor on the whole subscription, which must be dedicated to the region.
# AKS puts the region's node resources in a group of its own that must not exist before the
# cluster does, so no narrower scope can be granted up front. Contributor cannot write role
# assignments, and the identity is an Entra app registration, which lives in the tenant rather
# than in a resource group, so the grant cannot change its own federated credentials.

data "azuread_client_config" "current" {}

locals {
  # Restate Cloud's answer when it created the region (restate.tf), or what was passed in.
  region_tag         = var.restate != null ? try(local.grant_inputs.regionTag, null) : var.region_tag
  composition_issuer = var.restate != null ? try(local.grant_inputs.compositionIssuer, null) : var.composition_issuer
  restate_issuer     = var.restate != null ? try(local.grant_inputs.restateIssuer, null) : var.restate_issuer
  region_subject     = var.restate != null ? try(local.grant_inputs.regionSubject, null) : var.region_subject
  token_audience     = var.restate != null ? try(local.grant_inputs.tokenAudience, var.token_audience) : var.token_audience
  # Known when Terraform plans, because it decides which federated credentials exist (restate.tf).
  region_identity = var.restate != null ? local.restate_region_identity : var.restate_issuer != null
}

# A region grant has no resource group. The composition creates one for each region, which the
# grant's Contributor role on the subscription allows (region_grant.tf).
resource "azurerm_resource_group" "region" {
  count    = local.grant_mode ? 0 : 1
  name     = var.resource_group
  location = var.location
  tags     = { for k, v in { "restate-cloud-region" = local.region_tag } : k => v if v != null }

  # The grant's resources are replaced without removing the old ones first (restate.tf).
  lifecycle {
    create_before_destroy = true
  }
}

moved {
  from = azurerm_resource_group.region
  to   = azurerm_resource_group.region[0]
}

resource "azuread_application" "grant" {
  display_name = "Restate Cloud BYOC"
  owners       = [data.azuread_client_config.current.object_id]

  lifecycle {
    create_before_destroy = true
  }
}

resource "azuread_service_principal" "grant" {
  client_id = azuread_application.grant.client_id
  owners    = [data.azuread_client_config.current.object_id]

  lifecycle {
    create_before_destroy = true
  }
}

resource "azuread_application_federated_identity_credential" "provider" {
  for_each       = (local.region_identity || local.grant_mode) ? toset([]) : toset(var.providers_service_accounts)
  application_id = azuread_application.grant.id
  display_name   = each.value
  issuer         = local.composition_issuer
  audiences      = ["api://AzureADTokenExchange"]
  subject        = "system:serviceaccount:crossplane-system:${each.value}"

  lifecycle {
    create_before_destroy = true
  }
}

resource "azuread_application_federated_identity_credential" "region" {
  count          = local.region_identity && !local.grant_mode ? 1 : 0
  application_id = azuread_application.grant.id
  display_name   = "restate-cloud-region"
  issuer         = local.restate_issuer
  audiences      = [local.token_audience]
  subject        = local.region_subject

  lifecycle {
    create_before_destroy = true
  }
}

resource "azurerm_role_assignment" "grant" {
  scope                = "/subscriptions/${var.subscription_id}"
  role_definition_name = "Contributor"
  principal_id         = azuread_service_principal.grant.object_id
  principal_type       = "ServicePrincipal"

  lifecycle {
    create_before_destroy = true
  }
}
