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

# A region that signs in through the composition cluster, planned again with region_identity unset
# as an older version of the module left it: the identity comes from the region in the state.
run "restate_composition_region" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-us", region_identity = false }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_aaaaaaaaaa"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-us-aaaaaaaaaa\",\"regionTag\":\"rgn_aaaaaaaaaa\",\"compositionIssuer\":\"https://oidc.eks.us-east-2.amazonaws.com/id/ABC\"}}"
    }
  }
}

run "restate_composition_region_planned_again" {
  command = plan

  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-us" }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_aaaaaaaaaa"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-us-aaaaaaaaaa\",\"regionTag\":\"rgn_aaaaaaaaaa\",\"compositionIssuer\":\"https://oidc.eks.us-east-2.amazonaws.com/id/ABC\"}}"
    }
  }

  assert {
    condition     = length(azuread_application_federated_identity_credential.provider) == 5 && length(azuread_application_federated_identity_credential.region) == 0 && azurerm_resource_group.region[0].tags["restate-cloud-region"] == "rgn_aaaaaaaaaa"
    error_message = "An existing region must keep the composition cluster's credentials and its tag."
  }

  assert {
    condition     = !contains(keys(jsondecode(restapi_object.reservation[0].data)), "identity")
    error_message = "A region that was not asked to have its own identity must not send one."
  }
}
