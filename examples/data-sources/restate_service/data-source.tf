# Services are created and destroyed by deployment discovery, never by Terraform. This reads one.

data "restate_service" "order_service" {
  name = "OrderService"

  # A service exists only once a deployment exposing it has been registered.
  depends_on = [restate_deployment.orders]
}

output "service_revision" {
  value = data.restate_service.order_service.revision
}

output "handlers" {
  value = data.restate_service.order_service.handler_names
}

resource "restate_deployment" "orders" {
  http = {
    uri = "https://orders.internal.example.com:9080"
  }
}
