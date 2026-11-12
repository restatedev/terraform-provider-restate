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

variables {
  # terraform test fails an apply run whose ephemeral variable is unset, so the key the
  # module ignores without `restate` is set here. A real plan and apply need nothing.
  restate_api_key = "key_unused"
  subscription_id = "00000000-0000-0000-0000-000000000000"
  location        = "swedencentral"
}

# The credentials of a grant made before regions had their own identity, exactly as they were.
run "composition_by_hand" {
  variables {
    region_tag         = "rgn_x5z2y4qjmt"
    composition_issuer = "https://oidc.eks.us-east-2.amazonaws.com/id/ABC"
  }

  assert {
    condition     = toset(keys(azuread_application_federated_identity_credential.provider)) == toset(var.providers_service_accounts) && length(azuread_application_federated_identity_credential.region) == 0
    error_message = "Each provider ServiceAccount must keep its own credential."
  }
  assert {
    condition = alltrue([for sa, c in azuread_application_federated_identity_credential.provider :
      c.issuer == "https://oidc.eks.us-east-2.amazonaws.com/id/ABC" && c.subject == "system:serviceaccount:crossplane-system:${sa}" && c.audiences == tolist(["api://AzureADTokenExchange"])
    ])
    error_message = "The composition cluster's credentials changed."
  }
  assert {
    condition     = azurerm_resource_group.region[0].tags == tomap({ "restate-cloud-region" = "rgn_x5z2y4qjmt" })
    error_message = "The resource group must keep its region tag."
  }
}

run "region_identity_by_hand" {
  variables {
    restate_issuer = "https://oidc.restate.cloud"
    region_subject = "region:rgn_x5z2y4qjmt"
  }

  assert {
    condition     = length(azuread_application_federated_identity_credential.provider) == 0 && length(azuread_application_federated_identity_credential.region) == 1
    error_message = "A region with its own identity needs one credential."
  }
  assert {
    condition = (azuread_application_federated_identity_credential.region[0].issuer == "https://oidc.restate.cloud"
      && azuread_application_federated_identity_credential.region[0].subject == "region:rgn_x5z2y4qjmt"
    && azuread_application_federated_identity_credential.region[0].audiences == tolist(["api://AzureADTokenExchange"]))
    error_message = "The credential must accept only the region's subject from Restate's issuer."
  }
  assert {
    condition     = length(azurerm_resource_group.region[0].tags) == 0
    error_message = "A region with its own identity needs no region tag."
  }
}
