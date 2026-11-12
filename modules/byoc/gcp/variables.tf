variable "project" {
  description = "The project the region is built in. It must be dedicated to the region."
  type        = string
}

variable "identity_project" {
  description = <<-EOT
    The project that holds the service account and its workload identity pool. Defaults to the region project. A separate project is the
    hardened layout: the account administers the region project's service accounts, and in that
    project it would also administer itself.
  EOT
  type        = string
  default     = null
}

variable "region_name" {
  description = <<-EOT
    The region name Restate Cloud provides. The grant trusts the region's
    bootstrap job, which is named after it, to sign in to the region's cluster once. Leave unset
    with `restate`, which creates the region and takes its name.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.region_name == null || can(regex("^[a-z][a-z0-9-]{1,50}$", var.region_name))
    error_message = "region_name must be the claim name Restate Cloud provided."
  }
}

variable "region_tag" {
  description = <<-EOT
    The region tag Restate Cloud provides for an older region. The project is labelled
    restate-cloud-region-<tag> with it, and the composition builds nothing until it reads it.
    Leave unset with `restate`, which creates the region and takes its tag.
  EOT
  type        = string
  default     = null

  validation {
    # A label key is at most 63 characters, and this one is the tag behind a 21-character prefix.
    condition     = var.region_tag == null || can(regex("^[a-z0-9][a-z0-9_-]{0,41}$", var.region_tag))
    error_message = "region_tag must be the value Restate Cloud provided."
  }
}

variable "location" {
  description = "The region's location, for example europe-west1. Only with `restate`."
  type        = string
  default     = null
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

variable "provider_service_accounts" {
  description = <<-EOT
    The ServiceAccounts provider-gcp runs as on the composition cluster, one per provider in
    restatedev/restate-cloud's byoc/package-contract.json. The defaults match what Restate Cloud
    runs. The pool accepts a token from these and no other.
  EOT
  type        = list(string)
  default = [
    "provider-gcp-cloudplatform",
    "provider-gcp-compute",
    "provider-gcp-container",
    "provider-gcp-storage",
  ]
}

variable "pool_id" {
  description = <<-EOT
    The workload identity pool this creates. It trusts nothing but Restate Cloud. The default is
    restate-cloud in the region's project, restate-<region> in a separate identity_project, and
    restate-<resource prefix> for a region grant in either project.
  EOT
  type        = string
  default     = null
}

variable "service_account_id" {
  description = <<-EOT
    The account ID of the service account Restate Cloud impersonates. The default is
    restate-cloud-byoc in the region's project, restate-<region> in a separate identity_project, and
    restate-<resource prefix> for a region grant in either project.
  EOT
  type        = string
  default     = null
}

variable "restate_issuer" {
  description = <<-EOT
    The OIDC issuer that signs the region's own identity, for example https://oidc.restate.cloud.
    Restate Cloud provides it for a region that signs in with its own identity, and then gives no
    region tag or composition_issuer. Leave unset with `restate`, which takes it from the region.
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
  description = "The audience the pool's provider accepts for the region's identity token. Restate Cloud provides it together with restate_issuer, and the default is the same value."
  type        = string
  default     = "restate-cloud-region"
}
