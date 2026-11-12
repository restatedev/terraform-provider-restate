# examples/resources/ is one Terraform module: each file is named after the resource its
# documentation page shows, and they reference each other directly. This file holds the values a
# real configuration would supply, and is not rendered into the documentation.

variable "orders_api_key" {
  description = "API key the order service requires on every request Restate makes to it."
  type        = string
  sensitive   = true
}

variable "kafka_password" {
  description = "SASL password Restate authenticates to the Kafka brokers with."
  type        = string
  sensitive   = true
}

variable "aws_grant" {
  description = "The `grant` output of the grant module in modules/byoc: roleArn and permissionsBoundary."
  type        = map(string)
}
