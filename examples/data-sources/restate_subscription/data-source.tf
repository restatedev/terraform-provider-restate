data "restate_subscription" "existing" {
  id = "sub_1a2b3c"
}

output "source" {
  value = data.restate_subscription.existing.source
}
