# The Admin API has no update operation for subscriptions, so every change replaces this resource.
# Replacement briefly stops consumption and resumes from whatever offset `options` specifies.

resource "restate_subscription" "orders" {
  source = "kafka://${restate_kafka_cluster.events.name}/orders"
  sink   = "service://OrderService/handleOrder"

  options = {
    "auto.offset.reset" = "earliest"
    "group.id"          = "restate-orders"
  }

  # A sink must name a service that deployment discovery has already found.
  depends_on = [restate_deployment.orders]
}
