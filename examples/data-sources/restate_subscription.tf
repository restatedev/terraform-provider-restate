# Read a subscription created outside this configuration -- by the Restate CLI, or by an earlier
# Terraform state. `options` includes the defaults the server applied, not only what was requested.

data "restate_subscription" "orders" {
  id = "sub_1a2b3c"
}

output "consumes" {
  value = data.restate_subscription.orders.source
}

output "invokes" {
  value = data.restate_subscription.orders.sink
}
