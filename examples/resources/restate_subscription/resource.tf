# The Admin API has no update operation for subscriptions, so every change replaces this resource.
# Replacement briefly stops consumption and resumes from whatever offset `options` specifies.

resource "restate_subscription" "orders" {
  source = "kafka://${restate_kafka_cluster.orders.name}/orders"
  sink   = "service://${data.restate_service.order_service.name}/handleOrder"

  options = {
    "auto.offset.reset" = "earliest"
    "group.id"          = "restate-orders"
  }
}

resource "restate_kafka_cluster" "orders" {
  name = "orders-cluster"

  properties = {
    "bootstrap.servers" = "broker-1.kafka.example.com:9092"
  }
}

# Referencing the service through a data source makes the ordering explicit: a subscription's sink
# must name a service that deployment discovery has already found.
data "restate_service" "order_service" {
  name = "OrderService"

  depends_on = [restate_deployment.orders]
}

resource "restate_deployment" "orders" {
  http = {
    uri = "https://orders.internal.example.com:9080"
  }
}
