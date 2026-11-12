variable "subscription_id" {
  description = "The subscription the region, or with `region_grant` every region of the grant, is built in. It must be dedicated to them, because the grant is Contributor on all of it."
  type        = string
}

variable "tenant_id" {
  description = "The Entra tenant the subscription belongs to. Unset uses the tenant of the credentials Terraform runs with."
  type        = string
  default     = null
}

variable "location" {
  description = "The region's location, for example westeurope. Not used with `region_grant`, because each region of a grant chooses its own."
  type        = string
  default     = null

  validation {
    condition     = var.location != null || var.region_grant != null
    error_message = "location is required unless region_grant is set."
  }
}

variable "resource_group" {
  description = "The resource group the region lives in. The composition adopts it without changing it."
  type        = string
  default     = "restate-byoc-region"
}

variable "region_tag" {
  description = <<-EOT
    The region tag Restate Cloud provides. The resource group is tagged restate-cloud-region
    with it, and the composition builds nothing until it reads it.
    Leave unset with `restate`, which creates the region and takes its tag.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.region_tag == null || can(regex("^[a-z0-9][a-z0-9_-]{0,62}$", var.region_tag))
    error_message = "region_tag must be the value Restate Cloud provided."
  }
}

variable "composition_issuer" {
  description = <<-EOT
    The OIDC issuer of Restate Cloud's composition cluster, for example
    https://oidc.eks.us-east-2.amazonaws.com/id/<id>. Restate Cloud provides it. Leave unset with
    `restate`, which takes it from the region.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.composition_issuer == null ? true : startswith(var.composition_issuer, "https://")
    error_message = "composition_issuer must be an https URL."
  }
}

variable "providers_service_accounts" {
  description = <<-EOT
    The ServiceAccounts provider-azure runs as on the composition cluster: the family, and one per
    provider in restatedev/restate-cloud's byoc/package-contract.json. The defaults match what
    Restate Cloud runs. Each needs its own federated credential, because Entra matches the
    token's subject exactly.
  EOT
  type        = list(string)
  default = [
    # The family serves ResourceGroup, which a Grant region observes, so it calls Azure too.
    "provider-family-azure",
    "provider-azure-containerservice",
    "provider-azure-managedidentity",
    "provider-azure-network",
    "provider-azure-storage",
  ]
}

variable "restate_issuer" {
  description = <<-EOT
    The OIDC issuer that signs the region's own identity, for example https://oidc.restate.cloud.
    Restate Cloud provides it for a region that signs in with its own identity, and then gives no
    region_tag or composition_issuer. Leave unset with `restate`, which takes it from the region.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.restate_issuer == null || can(regex("^https://[^/]+", var.restate_issuer))
    error_message = "restate_issuer must be an https URL."
  }
}

variable "region_subject" {
  description = "The subject of the region's identity, region:<region-id>. Restate Cloud provides it together with restate_issuer."
  type        = string
  default     = null

  validation {
    condition     = var.region_subject == null || can(regex("^region:rgn_[a-z0-9]+$", var.region_subject))
    error_message = "region_subject must be region:<region-id>, as Restate Cloud provided it."
  }
}

variable "token_audience" {
  description = "The audience of the region's identity token. Restate Cloud provides it together with restate_issuer, and the default is the same value."
  type        = string
  default     = "api://AzureADTokenExchange"
}
