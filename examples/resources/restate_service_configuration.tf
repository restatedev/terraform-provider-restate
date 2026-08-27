# This resource manages settings on a service that discovery created; it neither creates nor deletes
# the service. Depend on the deployment that exposes it.
#
# Removing this resource, or removing an attribute from it, stops Terraform managing that setting and
# LEAVES THE APPLIED VALUE IN PLACE. The Admin API has no operation that resets a setting to its
# discovered value, so nothing is reverted. To restore SDK defaults, re-register the deployment with
# `overwrite = true`.

resource "restate_service_configuration" "order_service" {
  service_name = "OrderService"

  # Only reachable from other services, not from the environment's ingress.
  public = false

  # Durations may be written in the friendly form or as ISO-8601; the provider compares them by
  # meaning, so the server echoing one back in the other form is not a diff.
  journal_retention     = "7 days"
  idempotency_retention = "1 day"
  inactivity_timeout    = "1 minute"
  abort_timeout         = "1 minute"

  depends_on = [restate_deployment.orders]
}

# workflow_completion_retention applies only to a workflow service. The provider checks the service's
# actual type and errors rather than sending a setting the service cannot have.
resource "restate_service_configuration" "onboarding_workflow" {
  service_name = "OnboardingWorkflow"

  workflow_completion_retention = "P30D"
  journal_retention             = "P7D"

  depends_on = [restate_deployment.orders]
}
