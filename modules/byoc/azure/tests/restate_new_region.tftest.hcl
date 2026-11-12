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
  subscription_id = "00000000-0000-0000-0000-000000000000"
  location        = "swedencentral"
}

run "restate_new_region" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu", region_identity = true }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"api://AzureADTokenExchange\"}}"
    }
  }

  assert {
    condition     = length(azuread_application_federated_identity_credential.provider) == 0 && azuread_application_federated_identity_credential.region[0].subject == "region:rgn_x5z2y4qjmt" && azuread_application_federated_identity_credential.region[0].issuer == "https://oidc.dev.restate.cloud"
    error_message = "A new region must trust its own subject."
  }

  assert {
    condition     = jsondecode(restapi_object.reservation[0].data).identity == "region"
    error_message = "A new region must ask CreateRegion for its own identity."
  }

  assert {
    condition     = restapi_object.reservation[0].read_path == "/DescribeRegion" && jsondecode(restapi_object.reservation[0].read_data) == { name = var.restate.name }
    error_message = "The region must be read by its name, since repeating CreateRegion makes a new region once it is deleted."
  }

  assert {
    condition     = restapi_object.teardown[0].destroy_path == "/DeleteRegion" && jsondecode(restapi_object.teardown[0].destroy_data) == { regionId = restapi_object.reservation[0].id }
    error_message = "The teardown must delete the region, which keeps the grant until the region is gone."
  }
}
