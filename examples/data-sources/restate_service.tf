# Services are created and destroyed by deployment discovery, never by Terraform. This reads one so
# a configuration can depend on what discovery actually found.

data "restate_service" "order_service" {
  name = "OrderService"
}

# Turn a stale service name or a handler that was renamed in the SDK into a plan failure, rather than
# an apply that wires a subscription to a handler that is not there.
check "handle_order_exists" {
  assert {
    condition     = contains(data.restate_service.order_service.handler_names, "handleOrder")
    error_message = "OrderService does not expose handleOrder. Discovery may not have run since the service was renamed."
  }
}

output "service_revision" {
  value = data.restate_service.order_service.revision
}

output "handlers" {
  value = data.restate_service.order_service.handler_names
}
