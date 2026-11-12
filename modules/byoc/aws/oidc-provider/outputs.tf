output "arn" {
  description = "The provider's ARN. Pass it to every grant in the account as oidc_provider_arn."
  value       = aws_iam_openid_connect_provider.restate.arn
}
