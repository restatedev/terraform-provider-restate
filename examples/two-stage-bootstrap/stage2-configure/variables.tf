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
