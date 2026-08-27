# Stage 2: configure the environment stage 1 created.
#
# Nothing here references stage 1. The connection details arrive through RESTATE_ADMIN_URL and
# RESTATE_AUTH_TOKEN, which is what makes this a separate, cleanly configurable Terraform run.

terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

provider "restate" {}

# Assert the server is new enough before depending on anything version-specific. This also proves the
# credential works, before any resource is created.
data "restate_server_info" "environment" {}

check "server_is_recent_enough" {
  assert {
    condition     = data.restate_server_info.environment.max_admin_api_version >= 4
    error_message = "This configuration expects Admin API version 4 or newer. The server advertises up to ${data.restate_server_info.environment.max_admin_api_version}."
  }
}

# Registering a deployment performs service discovery: Restate calls this endpoint to learn which
# services and handlers it exposes. It must be reachable *from Restate*, not just from you.
resource "restate_deployment" "orders" {
  http = {
    uri = var.orders_uri

    # Sensitive, and preserved from configuration on refresh: a value the API declines to echo back
    # cannot silently erase the configured credential.
    additional_headers = var.orders_headers

    # Changing metadata replaces the deployment -- the Admin API's update request cannot carry it.
    metadata = {
      managed-by = "terraform"
    }
  }

  # Deleting a deployment can leave in-flight invocations unrecoverable, and forced deletion is the
  # only mode the Admin API implements. Terraform refuses to destroy this until the flag is applied.
  force_destroy = var.allow_deployment_deletion

  timeouts {
    create = "10m"
    update = "10m"
  }
}

# Reading the service back through a data source makes the ordering explicit: a service exists only
# once a deployment exposing it has been registered and discovered.
data "restate_service" "orders" {
  name       = var.orders_service_name
  depends_on = [restate_deployment.orders]
}

# Stable per-service configuration.
#
# Removing this resource, or an attribute from it, stops Terraform managing that setting and LEAVES
# THE APPLIED VALUE IN PLACE: the Admin API has no operation that resets a setting to its discovered
# value. To restore SDK defaults, re-register the deployment with overwrite = true.
resource "restate_service_configuration" "orders" {
  service_name = data.restate_service.orders.name

  # Reachable only from other services, not from the environment's ingress.
  public = false

  # Durations accept either the friendly form or ISO-8601; the provider compares them by meaning, so
  # the server echoing one back in the other form is not a diff.
  journal_retention  = "7 days"
  inactivity_timeout = "1 minute"
  abort_timeout      = "1 minute"
}

# Kafka is optional: set kafka_bootstrap_servers to enable it.
resource "restate_kafka_cluster" "events" {
  count = var.kafka_bootstrap_servers == null ? 0 : 1

  name = "events"

  properties = merge(
    {
      "bootstrap.servers" = var.kafka_bootstrap_servers
    },
    var.kafka_properties,
  )

  # Leave this false so the server's dependency check stays in charge: deleting a cluster that
  # subscriptions still reference would orphan them. Terraform destroys the subscription first
  # anyway, because it references the cluster.
  force_destroy = false
}

resource "restate_subscription" "orders" {
  count = var.kafka_bootstrap_servers == null ? 0 : 1

  source = "kafka://${restate_kafka_cluster.events[0].name}/${var.kafka_topic}"
  sink   = "service://${data.restate_service.orders.name}/${var.orders_handler_name}"

  options = {
    "auto.offset.reset" = "earliest"
  }
}

variable "orders_uri" {
  description = "Service endpoint Restate discovers and invokes. Must be reachable from Restate."
  type        = string
}

variable "orders_headers" {
  description = "Headers added to every discover and invoke request, typically an API key."
  type        = map(string)
  sensitive   = true
  default     = null
}

variable "orders_service_name" {
  description = "Name of a service the deployment exposes, as reported by discovery."
  type        = string
  default     = "OrderService"
}

variable "orders_handler_name" {
  description = "Handler the Kafka subscription invokes."
  type        = string
  default     = "handleOrder"
}

variable "allow_deployment_deletion" {
  description = <<-EOT
    Permit `terraform destroy` to delete the deployment.

    The Admin API implements only forced deletion, which can leave in-flight invocations in an
    unrecoverable state, so this has to be set and applied before a destroy will proceed.
  EOT
  type        = bool
  default     = false
}

variable "kafka_bootstrap_servers" {
  description = "Kafka brokers, enabling the Kafka cluster and subscription. Null leaves both out."
  type        = string
  default     = null
}

variable "kafka_properties" {
  description = <<-EOT
    Additional librdkafka properties, such as security.protocol and sasl.password.

    These are stored in Terraform state. The Admin API redacts them on read, so the provider keeps
    the configured values rather than overwriting them with placeholders.
  EOT
  type        = map(string)
  sensitive   = true
  default     = {}
}

variable "kafka_topic" {
  description = "Kafka topic the subscription consumes."
  type        = string
  default     = "orders"
}

output "deployment_id" {
  description = "Registered deployment ID, for `terraform import` and for the restate_deployment data source."
  value       = restate_deployment.orders.id
}

# Discovery output, exported for visibility. Terraform does not own it: re-registering the endpoint
# can change it.
output "discovered_services" {
  description = "Services this deployment exposes, as discovered."
  value       = restate_deployment.orders.services
}

output "service_revision" {
  description = "Current service revision. It changes when discovery finds a new schema, which is a useful signal that service configuration may have been overwritten."
  value       = data.restate_service.orders.revision
}

output "subscription_id" {
  description = "Kafka subscription ID, or null when Kafka is not configured."
  value       = try(restate_subscription.orders[0].id, null)
}
