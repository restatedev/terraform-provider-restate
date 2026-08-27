# Clusters are addressed by name, which is their identity.
#
# Sensitive property values come back redacted and an import has no configured values to fall back
# on, so state holds the API's placeholders rather than real credentials. Put the real values in your
# configuration and apply before relying on the cluster; the provider warns about this on import.
terraform import restate_kafka_cluster.orders orders-cluster
