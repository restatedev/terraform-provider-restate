# One region and its grant. Run with AWS credentials for the account and region it is built in.
module "restate_region" {
  source = "../../../aws"

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
