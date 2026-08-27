# Sensitive property values come back redacted, and this data source has no configured values to
# reconcile them against, so `properties` reports the API's placeholders rather than real
# credentials. Do not feed them to anything expecting a working password.

data "restate_kafka_cluster" "events" {
  name = "events"
}

# A cluster with subscriptions attached cannot be deleted without orphaning them, so this is what
# makes a safe teardown order expressible.
output "attached_subscriptions" {
  value = data.restate_kafka_cluster.events.subscription_ids
}
