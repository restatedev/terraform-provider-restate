# examples/data-sources/ is one Terraform module: each file is named after the data source its
# documentation page shows. This file holds the values a real configuration would supply, and is not
# rendered into the documentation.

variable "environment_id" {
  description = "Environment to look up, for example env_1a2b3c."
  type        = string
}

variable "restate_environment_api_key" {
  description = "Environment-scoped API key, from the secret store the provisioning stage wrote it to."
  type        = string
  sensitive   = true
}

variable "region" {
  description = "Region to place the environment in."
  type        = string
  default     = "us-east-2"
}
