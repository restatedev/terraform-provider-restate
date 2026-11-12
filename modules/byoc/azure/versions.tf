terraform {
  required_version = ">= 1.10"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
    azuread = {
      source  = "hashicorp/azuread"
      version = "~> 3.0"
    }
    restapi = {
      source  = "Mastercard/restapi"
      version = "3.0.0"
    }
    restate = {
      source = "restatedev/restate"
    }
  }
}

# Run with credentials that may create role assignments in the subscription and app registrations
# in the tenant. azurerm also registers the resource providers the region needs, such as
# Microsoft.ContainerService.
provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  tenant_id       = var.tenant_id
}

provider "azuread" {
  tenant_id = var.tenant_id
}
