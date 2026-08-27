# Confirm a handler exists before wiring a subscription to it, so a typo fails the plan rather than
# the apply. A handler's own retention and timeout settings are reported here only when they override
# the service's.

data "restate_service_handler" "handle_order" {
  service = "OrderService"
  name    = "handleOrder"
}

resource "restate_subscription" "orders" {
  source = "kafka://events/orders"
  sink   = "service://${data.restate_service_handler.handle_order.service}/${data.restate_service_handler.handle_order.name}"
}
