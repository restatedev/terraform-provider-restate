output "deployment_id" {
  description = "Registered deployment ID, for `terraform import` and for the restate_deployment data source."
  value       = restate_deployment.orders.id
}

# Discovery output, exported for visibility. Terraform does not own it: re-registering the endpoint
# can change it.
output "discovered_services" {
  description = "Services this deployment exposes, as discovered."
  value       = restate_deployment.orders.services
}

output "service_revision" {
  description = "Current service revision. It changes when discovery finds a new schema, which is a useful signal that service configuration may have been overwritten."
  value       = data.restate_service.orders.revision
}

output "subscription_id" {
  description = "Kafka subscription ID, or null when Kafka is not configured."
  value       = try(restate_subscription.orders[0].id, null)
}
