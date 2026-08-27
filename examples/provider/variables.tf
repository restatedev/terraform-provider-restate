# Supporting configuration for the snippet above. Not rendered into the documentation.

variable "restate_cloud_management_api_key" {
  description = "Account-scoped API key with role rst:role::ManagementApiAccess."
  type        = string
  sensitive   = true
}

variable "restate_environment_api_key" {
  description = "Environment-scoped API key with role rst:role::AdminAccess or rst:role::FullAccess."
  type        = string
  sensitive   = true
}
