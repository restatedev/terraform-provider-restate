# Properties can contain SASL passwords and TLS key material, so the whole map is sensitive and is
# stored in Terraform state. Use an encrypted, access-controlled state backend.
#
# The Admin API redacts sensitive values when it reads a cluster back, so the provider keeps the
# configured value for anything it cannot verify. Non-secret properties are still compared against
# the API, so a genuine out-of-band change shows as drift.

resource "restate_kafka_cluster" "events" {
  name = "events"

  properties = {
    "bootstrap.servers" = "broker-1.kafka.example.com:9092,broker-2.kafka.example.com:9092"
    "security.protocol" = "SASL_SSL"
    "sasl.mechanism"    = "SCRAM-SHA-512"
    "sasl.username"     = "restate"
    "sasl.password"     = var.kafka_password
  }

  # Leave this false so the API's own dependency check stays in charge: deleting a cluster that
  # subscriptions still reference would orphan them. Delete the subscriptions instead.
  force_destroy = false
}
