# The provider talks to two independent APIs with two separate credentials.
#
# The Cloud Management API provisions environments and environment API keys. The per-environment
# Admin API registers deployments and manages Kafka clusters, subscriptions, and service
# configuration. A management key is never sent to an environment, and an environment key is never
# sent to the Cloud Management API.
#
# Cloud-only, Admin-only, and combined configurations are all valid. See the two-stage bootstrap
# guide for why provisioning an environment and configuring it are separate stages.

terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

# Stage 1: provision environments and keys.
provider "restate" {
  account_id         = var.restate_cloud_account_id
  management_api_key = var.restate_cloud_management_api_key
}

# Stage 2: configure one existing environment. The URL and key come from stage 1's outputs, passed
# through variables or a secret store -- not through a reference in the same apply.
provider "restate" {
  alias = "production"

  admin_url           = var.restate_admin_url
  environment_api_key = var.restate_environment_api_key
}

variable "restate_cloud_account_id" {
  description = "Restate Cloud account ID, for example acc_1a2b3c. Also read from RESTATE_CLOUD_ACCOUNT_ID."
  type        = string
}

variable "restate_cloud_management_api_key" {
  description = "Account-scoped API key with role rst:role::ManagementApiAccess. Also read from RESTATE_CLOUD_MANAGEMENT_API_KEY."
  type        = string
  sensitive   = true
}

variable "restate_admin_url" {
  description = "Complete Admin API base URL of an existing environment. Also read from RESTATE_ADMIN_URL."
  type        = string
  default     = null
}

variable "restate_environment_api_key" {
  description = "Environment-scoped API key with role rst:role::AdminAccess or rst:role::FullAccess. Also read from RESTATE_AUTH_TOKEN."
  type        = string
  sensitive   = true
  default     = null
}
