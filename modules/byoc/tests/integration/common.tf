terraform {
  required_providers {
    time    = { source = "hashicorp/time", version = "~> 0.12" }
    restate = { source = "restatedev/restate" }
  }
}

variable "port" {
  type = number
}

variable "region_grant" {
  type = object({
    owner        = optional(string, "ACCOUNT")
    display_name = optional(string)
    regions = optional(map(object({
      cloud_region        = optional(string)
      display_name        = optional(string)
      machine_type        = optional(string)
      deletion_protection = optional(bool, true)
    })), {})
  })
}

provider "restate" {
  cloud_api_url       = "http://127.0.0.1:${var.port}/cloud"
  account_id          = "acc_1"
  management_api_key  = "key_test"
  allow_insecure_http = true
}

output "grant_id" {
  value = restate_cloud_region_grant.this[0].id
}

output "status" {
  value = restate_cloud_region_grant_confirmation.this[0].status
}
