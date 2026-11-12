mock_provider "azurerm" {}

mock_provider "azuread" {
  override_data {
    target = data.azuread_client_config.current
    values = { tenant_id = "11111111-1111-1111-1111-111111111111", object_id = "22222222-2222-2222-2222-222222222222" }
  }
  mock_resource "azuread_application" {
    defaults = {
      id        = "/applications/33333333-3333-3333-3333-333333333333"
      client_id = "44444444-4444-4444-4444-444444444444"
    }
  }
  mock_resource "azuread_service_principal" {
    defaults = { object_id = "55555555-5555-5555-5555-555555555555" }
  }
}

mock_provider "restapi" {}

# What Restate Cloud answers for a new grant, as the restate provider exports it.
mock_provider "restate" {
  mock_resource "restate_cloud_region_grant" {
    defaults = {
      id              = "rgr_x5z2y4qjmt"
      owner_id        = "acc_1a2b3c4d5e"
      status          = "PENDING"
      restate_issuer  = "https://oidc.dev.restate.cloud"
      grant_subject   = "grant:rgr_x5z2y4qjmt"
      token_audience  = "api://AzureADTokenExchange"
      resource_prefix = "rgr-x5z2y4qjmt"
    }
  }
  mock_resource "restate_cloud_region_grant_confirmation" {
    defaults = { id = "rgr_x5z2y4qjmt", status = "READY" }
  }
  mock_resource "restate_cloud_byoc_region" {
    defaults = { id = "rgn_a1b2c3d4e5", status = "ACTIVE" }
  }
}

variables {
  subscription_id = "ABCDEF01-0000-0000-0000-000000000000"
  # terraform test fails an apply run whose ephemeral variable is unset, so the key is set here
  # even for the runs that do not use it.
  restate_api_key = "key_unused"
}

run "account_grant" {
  variables {
    location = "swedencentral"
    region_grant = {
      regions = {
        prod-eu = {}
        prod-us = { cloud_region = "eastus2", machine_type = "Standard_D4ps_v6", deletion_protection = false }
      }
    }
  }

  assert {
    condition = (
      restate_cloud_region_grant.this[0].owner == "ACCOUNT" &&
      restate_cloud_region_grant.this[0].cloud == "azure" && restate_cloud_region_grant.this[0].cloud_account_id == "abcdef01-0000-0000-0000-000000000000"
    )
    error_message = "The grant must name the owner, the cloud and the lower-case subscription."
  }

  assert {
    condition     = length(azurerm_resource_group.region) == 0
    error_message = "A region grant creates no resource group."
  }

  assert {
    condition     = length(azuread_application_federated_identity_credential.provider) == 0 && length(azuread_application_federated_identity_credential.region) == 0 && length(azuread_application_federated_identity_credential.grant) == 1
    error_message = "A region grant needs one federated credential and none for a region or the composition cluster."
  }

  assert {
    condition = (azuread_application_federated_identity_credential.grant[0].subject == "grant:rgr_x5z2y4qjmt"
      && azuread_application_federated_identity_credential.grant[0].issuer == "https://oidc.dev.restate.cloud"
    && azuread_application_federated_identity_credential.grant[0].audiences == tolist(["api://AzureADTokenExchange"]))
    error_message = "The credential must accept only the grant's subject from Restate's issuer with the grant's audience."
  }

  assert {
    condition     = azurerm_role_assignment.grant.role_definition_name == "Contributor" && azurerm_role_assignment.grant.scope == "/subscriptions/ABCDEF01-0000-0000-0000-000000000000"
    error_message = "The grant must be Contributor on the subscription."
  }

  # Confirmation and regions
  assert {
    condition = (restate_cloud_region_grant_confirmation.this[0].grant_id == "rgr_x5z2y4qjmt" && restate_cloud_region_grant_confirmation.this[0].grant == tomap({
      tenantId = "11111111-1111-1111-1111-111111111111"
      clientId = "44444444-4444-4444-4444-444444444444"
    }))
    error_message = "The confirmation must carry the tenant and the client id, and no resource group."
  }

  assert {
    condition     = restate_cloud_region_grant_confirmation.this[0].timeouts.create == "15m"
    error_message = "The confirmation must keep trying for as long as Entra ID and role assignments can take to spread."
  }

  assert {
    condition = (
      restate_cloud_byoc_region.this["prod-eu"].cloud_region == "swedencentral" && restate_cloud_byoc_region.this["prod-us"].cloud_region == "eastus2" &&
      restate_cloud_byoc_region.this["prod-eu"].grant_id == "rgr_x5z2y4qjmt" &&
      restate_cloud_byoc_region.this["prod-us"].machine_type == "Standard_D4ps_v6" &&
      restate_cloud_byoc_region.this["prod-eu"].deletion_protection && !restate_cloud_byoc_region.this["prod-us"].deletion_protection
    )
    error_message = "Each region must be built with the confirmed grant, in the module's location unless it names another."
  }

  assert {
    condition     = length(restapi_object.api_key) == 0 && length(restapi_object.reservation) == 0
    error_message = "In grant mode the module must make no restapi call."
  }

  assert {
    condition = (output.grant_id == "rgr_x5z2y4qjmt" && output.resource_prefix == "rgr-x5z2y4qjmt" && output.status == "READY"
      && output.region_ids == { prod-eu = "rgn_a1b2c3d4e5", prod-us = "rgn_a1b2c3d4e5" }
      && output.grant == { tenantId = "11111111-1111-1111-1111-111111111111", clientId = "44444444-4444-4444-4444-444444444444" }
    && output.region_id == null)
    error_message = "A region grant must output its id, prefix, status, regions and the grant without a resource group."
  }
}

run "organization_grant" {
  command = plan

  variables {
    region_grant = { owner = "ORGANIZATION", display_name = "Replit production" }
  }

  assert {
    condition     = restate_cloud_region_grant.this[0].owner == "ORGANIZATION" && restate_cloud_region_grant.this[0].display_name == "Replit production" && length(restate_cloud_byoc_region.this) == 0
    error_message = "An organization grant must name its owner and display name, and build no region unasked."
  }
}

run "exclusive_with_restate" {
  command = plan

  variables {
    location        = "swedencentral"
    region_grant    = {}
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = "key_test"
  }

  expect_failures = [var.region_grant]
}

run "exclusive_with_manual_inputs" {
  command = plan

  variables {
    region_grant       = {}
    region_tag         = "rgn_x5z2y4qjmt"
    composition_issuer = "https://oidc.eks.us-east-2.amazonaws.com/id/ABC"
  }

  expect_failures = [var.region_grant]
}

run "region_needs_a_location" {
  command = plan

  variables {
    region_grant = { regions = { prod-eu = {} } }
  }

  expect_failures = [restate_cloud_byoc_region.this]
}

run "grant_ignores_a_module_credential" {
  command = plan

  variables {
    region_grant         = {}
    restate_api_key      = null
    restate_access_token = "token_test"
  }

  expect_failures = [check.region_grant_credential]
}

run "both_credentials_are_refused" {
  command = plan

  variables {
    location             = "swedencentral"
    restate              = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key      = "key_test"
    restate_access_token = "token_test"
  }

  expect_failures = [var.restate_access_token]
}

run "restate_excludes_the_manual_inputs" {
  command = plan

  variables {
    location        = "swedencentral"
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = "key_test"
    region_tag      = "rgn_x5z2y4qjmt"
  }

  expect_failures = [var.restate]
}

run "restate_needs_a_credential" {
  command = plan

  variables {
    location        = "swedencentral"
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = null
  }

  expect_failures = [var.restate]
}

run "grant_owner_is_validated" {
  command = plan

  variables {
    region_grant = { owner = "TEAM" }
  }

  expect_failures = [var.region_grant]
}

run "location_is_required_without_a_grant" {
  command = plan

  variables {
    restate_issuer = "https://oidc.restate.cloud"
    region_subject = "region:rgn_x5z2y4qjmt"
  }

  expect_failures = [var.location]
}
