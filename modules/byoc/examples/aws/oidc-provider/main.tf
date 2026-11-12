# The account's OIDC provider for Restate's issuer, in a state of its own, because every region
# grant in the account trusts it.
module "restate_oidc_provider" {
  source = "../../../aws/oidc-provider"

  restate_issuer = var.restate_issuer
}

variable "restate_issuer" {
  type    = string
  default = "https://oidc.restate.cloud"
}

output "arn" {
  value = module.restate_oidc_provider.arn
}
