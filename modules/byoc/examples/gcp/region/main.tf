# One region and its grant, in a project dedicated to the region.
module "restate_region" {
  source = "../../../gcp"

  project          = var.project
  identity_project = var.identity_project
  location         = var.location
  restate = {
    account_id = var.restate_account_id
    name       = var.region_name
    # true only where Restate Cloud gives new regions their own identity
    region_identity = false
  }
  restate_api_key      = var.restate_api_key
  restate_access_token = var.restate_access_token
}

variable "restate_account_id" {
  type = string
}

variable "project" {
  type = string
}

# Optional. A second project for the service account and its pool, so that the service account
# cannot change its own access in the region project.
variable "identity_project" {
  type    = string
  default = null
}

variable "location" {
  type    = string
  default = "europe-west1"
}

variable "region_name" {
  type    = string
  default = "prod-eu"
}

# Set one of the two, as TF_VAR_restate_api_key or TF_VAR_restate_access_token.
variable "restate_api_key" {
  type      = string
  default   = null
  sensitive = true
  ephemeral = true
}

variable "restate_access_token" {
  type      = string
  default   = null
  sensitive = true
}

output "region_id" {
  value = module.restate_region.region_id
}
