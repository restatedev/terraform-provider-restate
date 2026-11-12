/^resource "azuread_application_federated_identity_credential" "grant" {/,/^}/d
s/azuread_application_federated_identity_credential\.grant\[0\]\.subject/terraform_data.credential.output/g
s/azuread_application_federated_identity_credential\.grant/terraform_data.credential/g
s/azuread_application\.grant\.client_id/terraform_data.application.output/g
s/azuread_application\.grant\.id/terraform_data.application.output/g
s/azuread_service_principal\.grant/terraform_data.principal/g
s/azurerm_role_assignment\.grant\.scope/terraform_data.assignment.output/g
s/azurerm_role_assignment\.grant/terraform_data.assignment/g
s/data\.azuread_client_config\.current\.tenant_id/"tenant"/g
