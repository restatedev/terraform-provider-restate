variable "restate_issuer" {
  description = "Restate's OIDC issuer, as the region grant's inputs name it (restateIssuer), for example https://oidc.restate.cloud."
  type        = string

  validation {
    condition     = can(regex("^https://[^/]+", var.restate_issuer))
    error_message = "restate_issuer must be an https URL."
  }
}

variable "token_audience" {
  description = "The audience of Restate's tokens (tokenAudience in the region grant's inputs). AWS requires it in the provider's client ID list."
  type        = string
  default     = "sts.amazonaws.com"
}
