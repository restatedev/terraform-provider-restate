# Read a deployment registered outside this configuration.
#
# Both endpoint kinds' attributes are exposed as a flat set; the ones belonging to the other kind are
# null, so check endpoint_type first. `additional_headers` is deliberately not exposed: header values
# are typically credentials and there is nothing to reconcile a redacted one against.

data "restate_deployment" "existing" {
  id = "dp_1a2b3c"
}

output "endpoint" {
  value = data.restate_deployment.existing.endpoint_type == "http" ? data.restate_deployment.existing.uri : data.restate_deployment.existing.arn
}

output "sdk_version" {
  value = data.restate_deployment.existing.sdk_version
}
