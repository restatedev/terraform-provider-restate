# Version and capability information for the Restate server behind the provider's admin_url.

data "restate_server_info" "environment" {}

output "restate_version" {
  value = data.restate_server_info.environment.version
}

output "ingress_endpoint" {
  value = data.restate_server_info.environment.ingress_endpoint
}

# Assert a minimum server version before depending on a newer feature.
check "server_is_recent_enough" {
  assert {
    condition     = data.restate_server_info.environment.max_admin_api_version >= 4
    error_message = "This configuration needs Admin API version 4 or newer. The server advertises up to ${data.restate_server_info.environment.max_admin_api_version}."
  }
}
