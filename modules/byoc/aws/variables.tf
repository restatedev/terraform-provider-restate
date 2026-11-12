variable "restate_principals" {
  description = <<-EOT
    The roles in Restate Cloud's AWS account that may assume this grant. Restate Cloud provides
    the list. Leave unset with `restate`, which takes it from the region.
  EOT
  type        = list(string)
  default     = null

  validation {
    condition     = var.restate_principals == null ? true : length(var.restate_principals) > 0 && alltrue([for p in var.restate_principals : can(regex("^arn:aws:iam::[0-9]{12}:role/.+$", p))])
    error_message = "restate_principals must list IAM role ARNs."
  }
}

variable "external_id" {
  description = "The region's external id, as Restate Cloud provided it. It is unique to the region. Leave unset with `restate`, which creates the region and takes its id."
  type        = string
  default     = null

  validation {
    condition     = var.external_id == null || try(length(var.external_id) >= 2 && length(var.external_id) <= 1224, false)
    error_message = "external_id must be between 2 and 1224 characters, as STS requires."
  }
}

variable "region_name" {
  description = <<-EOT
    The region name Restate Cloud provides. The grant covers only resources whose names start
    with it. Leave unset with `restate`, which creates the region and takes its name.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.region_name == null || can(regex("^[a-z0-9][a-z0-9-]{0,40}$", var.region_name))
    error_message = "region_name must be lowercase letters, digits and hyphens, at most 41 characters."
  }
}

variable "role_name" {
  description = "The name of the role this creates, and of its boundary with -boundary added. The default is restate-cloud-byoc, or restate-cloud-<resource prefix> for a region grant. It must not start with region_name or the grant's resource prefix, so the grant cannot reach either."
  type        = string
  default     = null
}

variable "restate_issuer" {
  description = <<-EOT
    The OIDC issuer that signs the region's own identity, for example https://oidc.restate.cloud.
    Restate Cloud provides it for a region that signs in with its own identity, and then gives no
    external_id or restate_principals. Leave unset with `restate`, which takes it from the region.
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
  default     = "sts.amazonaws.com"
}

variable "oidc_provider_arn" {
  description = <<-EOT
    An IAM OIDC identity provider for restate_issuer that already exists in the account. AWS allows
    one provider for each issuer in an account, so when the account holds more than one region,
    create the provider once and pass its ARN to every grant. Unset, the grant creates the
    provider itself. Only for a region that signs in with its own identity. Required with
    `region_grant`, which never creates the provider; the oidc-provider module does.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.oidc_provider_arn == null || can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:oidc-provider/.+$", var.oidc_provider_arn))
    error_message = "oidc_provider_arn must be the ARN of an IAM OIDC identity provider."
  }
}
