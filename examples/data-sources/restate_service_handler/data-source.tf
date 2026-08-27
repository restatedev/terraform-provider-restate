# Confirm a handler exists before referencing it as a subscription sink, so a typo fails the plan
# rather than the apply.

data "restate_service_handler" "handle_order" {
  service = "OrderService"
  name    = "handleOrder"
}

output "handler_type" {
  value = data.restate_service_handler.handle_order.type
}
