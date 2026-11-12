# Optional: create a region grant instead of a region (var.region_grant, declared in restate.tf),
# through the caller's restate provider.
#
# A region grant belongs to one Restate account or organization and builds any number of regions in
# this subscription. `region_grant.regions` builds some here, and administrators of the owning
# account or organization can build more elsewhere with the grant's id. One apply:
#
# 1. creates the grant (restate_cloud_region_grant), which answers the issuer, the subject
#    grant:<grant id>, the token audience and the prefix of every resource name,
# 2. creates the app registration with one federated credential that accepts exactly that subject
#    from Restate's issuer, and the Contributor role on the subscription (main.tf),
# 3. confirms the grant (restate_cloud_region_grant_confirmation). Restate signs in through it and
#    reads the subscription, trying again in the background while Entra ID and Azure Resource
#    Manager catch up, and the confirmation waits until the grant is READY,
# 4. builds each region with the confirmed grant.
#
# The module creates no resource group. The composition creates one for each region, named with
# the grant's prefix, which Contributor on the subscription allows.
#
# A destroy runs backwards. The regions go first. The confirmation's destroy deletes the grant at
# Restate before the app registration and the role are touched, and Restate refuses while any region
# uses the grant, including regions built by other configurations, so the destroy stops and keeps
# them.

locals {
  grant_mode = var.region_grant != null

  region_grant_id = local.grant_mode ? restate_cloud_region_grant.this[0].id : null
  region_grant_inputs = !local.grant_mode ? null : {
    restateIssuer  = restate_cloud_region_grant.this[0].restate_issuer
    grantSubject   = restate_cloud_region_grant.this[0].grant_subject
    tokenAudience  = restate_cloud_region_grant.this[0].token_audience
    resourcePrefix = restate_cloud_region_grant.this[0].resource_prefix
  }

  # What Restate Cloud needs to sign in through the grant. The resource group is left out, because
  # the control plane adds the one it names for each region.
  grant_output = {
    tenantId = data.azuread_client_config.current.tenant_id
    clientId = azuread_application.grant.client_id
  }
}

# The API keeps one subscription id in lower case, so the grant names it the same way.
resource "restate_cloud_region_grant" "this" {
  count = local.grant_mode ? 1 : 0

  owner            = var.region_grant.owner
  display_name     = var.region_grant.display_name
  cloud            = "azure"
  cloud_account_id = lower(var.subscription_id)
}

# Entra ID accepts a token from this issuer and subject, and only that one, as the app registration.
resource "azuread_application_federated_identity_credential" "grant" {
  count          = local.grant_mode ? 1 : 0
  application_id = azuread_application.grant.id
  display_name   = "restate-cloud-region-grant"
  issuer         = local.region_grant_inputs.restateIssuer
  audiences      = [local.region_grant_inputs.tokenAudience]
  subject        = local.region_grant_inputs.grantSubject
}

# Microsoft documents no figure for a new federated credential. It says a token request made
# several minutes after the credential was created may still fail with AADSTS70021 while the
# directory's cache holds old data, and that a new role assignment can take up to 10 minutes to
# take effect. Restate keeps signing in for 20 minutes, and the confirmation waits until its create
# timeout, which is set to cover the documented delays. It depends on the service principal, the credential and the role assignment,
# none of which its grant output names.
resource "restate_cloud_region_grant_confirmation" "this" {
  count = local.grant_mode ? 1 : 0

  grant_id = restate_cloud_region_grant.this[0].id
  grant    = local.grant_output

  timeouts {
    create = "15m"
    update = "15m"
  }

  depends_on = [
    azuread_service_principal.grant,
    azuread_application_federated_identity_credential.grant,
    azurerm_role_assignment.grant,
  ]
}

resource "restate_cloud_byoc_region" "this" {
  for_each = local.grant_mode ? var.region_grant.regions : {}

  name                = each.key
  display_name        = each.value.display_name
  grant_id            = restate_cloud_region_grant_confirmation.this[0].grant_id
  cloud_region        = coalesce(each.value.cloud_region, var.location, "unset")
  machine_type        = each.value.machine_type
  deletion_protection = each.value.deletion_protection

  lifecycle {
    precondition {
      condition     = each.value.cloud_region != null || var.location != null
      error_message = "Region ${each.key} in region_grant.regions needs cloud_region, or the module's location."
    }
  }
}
