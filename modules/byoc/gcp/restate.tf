# Optional: create the region in your Restate Cloud account, and hand it this grant.
#
# With `restate` set, one apply creates the region (CreateRegion with setup GRANT), which answers
# what the grant needs, then creates the grant, then hands it to Restate Cloud
# (SetRegionGrant), which starts building the region. The project and location are this module's own, so the grant and
# the region cannot disagree about either.
#
# The answer is the region name and either the region's own identity (issuer, subject and audience)
# or the composition cluster's issuer and the region tag. Restate Cloud chooses once, when it
# creates the region, unless restate.region_identity is true, which asks for the region's own
# identity. With the region tag, the module labels the project with it (label.tf).
#
# A region grant is the alternative (region_grant): one grant in the project for any number of regions,
# created and confirmed through the caller's restate provider (region_grant.tf), so this file's restapi
# resources are unused. See the README.
#
# A destroy deletes the region first. Restate Cloud answers 409 until the teardown has finished, so
# the destroy fails and keeps the grant, which Restate needs to delete what it built. Run the destroy
# again until it succeeds. Environments in the region must be deleted before it can.
#
# Nothing else may remove the grant while the region exists. restapi_object.teardown holds it even
# when SetRegionGrant failed after it started a build, and the grant's resources are created before
# they are destroyed, so an apply that replaces one stops at SetRegionGrant's 409 with the old one
# still in place.

variable "restate" {
  description = <<-EOT
    The Restate Cloud account that owns the region: account_id, the region's name (unique within the
    account), an optional display name and an optional machine type (e2-standard-4 by default). api_url defaults to https://api.us.restate.cloud.
    region_identity is true for a new region that signs in with its own identity: the module asks Restate
    Cloud for it with CreateRegion, and a stage without an issuer refuses. Unset, Restate Cloud chooses when it
    creates the region, and the module follows its answer.
    Leave unset to pass the region's identifiers yourself.
  EOT
  type = object({
    api_url         = optional(string, "https://api.us.restate.cloud")
    account_id      = string
    name            = string
    display_name    = optional(string)
    machine_type    = optional(string, "e2-standard-4")
    region_identity = optional(bool)
  })
  default = null

  validation {
    condition     = var.restate == null || can(regex("^[a-z][a-z0-9-]{1,38}[a-z0-9]$", var.restate.name))
    error_message = "restate.name must be 3 to 40 lower-case letters, digits and hyphens, starting with a letter."
  }

  validation {
    condition     = var.restate == null || alltrue([for v in [var.region_name, var.region_tag, var.composition_issuer, var.restate_issuer, var.region_subject] : v == null])
    error_message = "With restate, leave region_name, region_tag, composition_issuer, restate_issuer and region_subject unset. The module takes them from the region."
  }

  validation {
    condition     = var.restate == null || var.restate_api_key != null || var.restate_access_token != null
    error_message = "restate needs restate_api_key or restate_access_token."
  }
}

variable "region_grant" {
  description = <<-EOT
    Create a region grant, which builds any number of regions in `project`, instead of a grant for one
    region, through the caller's `restate` provider. owner is ACCOUNT (the provider's account) or
    ORGANIZATION (its organization). `regions` builds regions with the grant, each in its cloud_region,
    for example europe-west1. Leave unset to use `restate`, or to pass one region's identifiers
    yourself.
  EOT
  type = object({
    owner        = optional(string, "ACCOUNT")
    display_name = optional(string)
    regions = optional(map(object({
      cloud_region        = string
      display_name        = optional(string)
      machine_type        = optional(string)
      deletion_protection = optional(bool, true)
    })), {})
  })
  default = null

  validation {
    condition     = var.region_grant == null ? true : contains(["ACCOUNT", "ORGANIZATION"], var.region_grant.owner)
    error_message = "region_grant.owner must be ACCOUNT or ORGANIZATION."
  }

  validation {
    condition = var.region_grant == null || (
      var.restate == null && alltrue([for v in [var.location, var.region_name, var.region_tag, var.composition_issuer, var.restate_issuer, var.region_subject] : v == null])
    )
    error_message = "region_grant cannot be combined with restate, location, region_name, region_tag, composition_issuer, restate_issuer or region_subject. Each of its regions names its own cloud_region."
  }
}

check "region_grant_credential" {
  assert {
    condition     = var.region_grant == null || var.restate_access_token == null
    error_message = "region_grant takes its credential from the restate provider, so restate_access_token is ignored. Configure the provider with it instead."
  }
}

variable "restate_api_key" {
  description = <<-EOT
    A Restate Cloud API key with the Management API role, for the account. Only with `restate`, and
    not with restate_access_token.
  EOT
  type        = string
  default     = null
  sensitive   = true
  ephemeral   = true
}

variable "restate_access_token" {
  description = <<-EOT
    Instead of restate_api_key: an account Administrator's access token from `restate cloud login`.
    The module then creates the Management API key itself, keeps it in the state and deletes it
    with the region. The token is short-lived and the key is read back with it, so pass a fresh
    one on every run. Only with `restate`.
  EOT
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.restate_access_token == null || var.restate_api_key == null
    error_message = "Set only one of restate_api_key and restate_access_token."
  }
}

provider "restapi" {
  # Unused without `restate`; a provider still needs an address to be configured.
  uri                  = local.restate_api
  bearer_token         = local.restate_api_key
  write_returns_object = true
  id_attribute         = "regionId"
  headers              = { "content-type" = "application/json" }
}

# Only an Administrator's own token can create account API keys, so the key has a provider of its own.
provider "restapi" {
  alias                = "login"
  uri                  = local.restate_api
  bearer_token         = var.restate_access_token
  write_returns_object = true
  # read_search, which reads the key back, takes the provider's method and not the resource's.
  read_method  = "POST"
  id_attribute = "keyId"
  headers      = { "content-type" = "application/json" }
}

# With restate_access_token: the Management API key the region calls go through. The account has
# no DescribeAccountApiKey, so both objects read the key back from ListAccountApiKeys.
resource "restapi_object" "api_key" {
  count    = var.restate != null && var.restate_access_token != null ? 1 : 0
  provider = restapi.login

  path        = "/ListAccountApiKeys"
  create_path = "/CreateAccountApiKey"
  data        = jsonencode({ roleId = "rst:role::ManagementApiAccess", description = "Managed BYOC region ${var.restate.name}" })
  read_search = {
    results_key  = "apiKeys"
    search_key   = "keyId"
    search_value = "{id}"
  }
  # The provider cannot put the key's own id into a destroy body, so api_key_deletion deletes it.
  destroy_path   = "/ListAccountApiKeys"
  destroy_method = "POST"

  force_new                 = ["roleId", "description"]
  ignore_all_server_changes = true
}

resource "restapi_object" "api_key_deletion" {
  count    = length(restapi_object.api_key)
  provider = restapi.login

  object_id = restapi_object.api_key[0].id
  path      = "/ListAccountApiKeys"
  data      = jsonencode({})
  read_search = {
    results_key  = "apiKeys"
    search_key   = "keyId"
    search_value = "{id}"
  }
  destroy_path   = "/DeleteAccountApiKey"
  destroy_method = "POST"
  destroy_data   = jsonencode({ keyId = restapi_object.api_key[0].id })

  ignore_all_server_changes = true
}

check "restate_inputs" {
  assert {
    condition = (var.restate != null ? 1 : 0) + (var.region_grant != null ? 1 : 0) + ((
      (var.region_name != null && var.composition_issuer != null && var.restate_issuer == null && var.region_subject == null) ||
      (var.restate_issuer != null && var.region_subject != null && var.composition_issuer == null)
    ) ? 1 : 0) == 1 && (var.restate == null || var.location != null)
    error_message = "Set one of restate (with location), region_grant, or the region's identifiers by hand: region_name and composition_issuer, or restate_issuer and region_subject."
  }
}

locals {
  restate_api = var.restate != null ? "${var.restate.api_url}/cloud/${var.restate.account_id}" : "https://restate.invalid"
  # Only the create response holds the key itself.
  created_api_key = one([for k in restapi_object.api_key : jsondecode(k.create_response).apiKey])
  restate_api_key = var.restate_access_token != null ? local.created_api_key : var.restate_api_key
  region_request = var.restate == null ? null : jsonencode(merge(
    {
      name        = var.restate.name
      displayName = coalesce(var.restate.display_name, var.restate.name)
      setup       = "GRANT"
      cloud       = "gcp"
      cloudRegion = var.location
      gcp         = { projectId = var.project }
    },
    var.restate.machine_type == null ? {} : { infrastructure = { machineType = var.restate.machine_type } },
    var.restate.region_identity == true ? { identity = "region" } : {},
  ))
  # What the grant needs, as Restate Cloud answered it.
  grant_inputs = var.restate == null ? null : jsondecode(restapi_object.reservation[0].api_response).grantInputs
}

# The API takes the region in the body, and the provider substitutes {id} only into paths, so the
# region is read and deleted by its name. Reading by repeating the create would make a new region
# once this one is deleted.
resource "restapi_object" "reservation" {
  count = var.restate != null ? 1 : 0

  path           = "/CreateRegion"
  data           = local.region_request
  read_path      = "/DescribeRegion"
  read_method    = "POST"
  read_data      = jsonencode({ name = var.restate.name })
  update_path    = "/CreateRegion"
  update_method  = "POST"
  update_data    = local.region_request
  destroy_path   = "/DeleteRegion"
  destroy_method = "POST"
  destroy_data   = jsonencode({ name = var.restate.name })

  # Another name, cloud or place is another region.
  force_new                 = ["name", "cloud", "cloudRegion", "gcp"]
  ignore_all_server_changes = true
  # As teardown's: a refresh after the region is deleted must not fail.
  ignore_server_additions = true

  lifecycle {
    postcondition {
      condition     = var.restate.region_identity == null || var.restate.region_identity == (try(jsondecode(self.api_response).grantInputs.restateIssuer, null) != null)
      error_message = "Restate Cloud chose the other identity for region ${var.restate.name}. Leave restate.region_identity unset, or set it to ${try(jsondecode(self.api_response).grantInputs.restateIssuer, null) != null}."
    }
  }

  # Kept until the region is gone, so a destroy that has to be run again can still reach the API.
  depends_on = [restapi_object.api_key_deletion]
}

# A failed create leaves nothing in the state, so a SetRegionGrant that failed after it started a
# build would leave restapi_object.region out of it, and a destroy would remove the grant while the
# region is being built. This object exists before SetRegionGrant is called, and deletes the region
# before the grant is removed.
resource "restapi_object" "teardown" {
  count = var.restate != null ? 1 : 0

  path           = "/DescribeRegion"
  data           = jsonencode({ regionId = restapi_object.reservation[0].id })
  read_path      = "/DescribeRegion"
  read_method    = "POST"
  read_data      = jsonencode({ regionId = restapi_object.reservation[0].id })
  update_path    = "/DescribeRegion"
  update_method  = "POST"
  destroy_path   = "/DeleteRegion"
  destroy_method = "POST"
  destroy_data   = jsonencode({ regionId = restapi_object.reservation[0].id })

  ignore_all_server_changes = true
  # restapi 3.0.0 fails a refresh whose read answers 404 unless this is set
  # (Mastercard/terraform-provider-restapi#377). With it, a deleted region stays in the state,
  # and the destroy's DeleteRegion answers DELETED.
  ignore_server_additions = true

  depends_on = [
    restapi_object.api_key_deletion,
    google_iam_workload_identity_pool_provider.composition,
    google_service_account_iam_member.federation,
    google_project_iam_member.grant,
    google_project_iam_member.grant_storage,
    google_project_iam_member.grant_iam,
    restapi_object.region_label,
  ]
}

resource "restapi_object" "region" {
  count = var.restate != null ? 1 : 0

  path = "/SetRegionGrant"
  data = jsonencode({
    regionId = restapi_object.reservation[0].id
    grant = {
      workloadIdentityProvider = google_iam_workload_identity_pool_provider.composition.name
      serviceAccount           = google_service_account.grant.email
      identityProject          = local.identity_project
    }
  })
  read_path   = "/DescribeRegion"
  read_method = "POST"
  read_data   = jsonencode({ regionId = restapi_object.reservation[0].id })
  # A repeat starts nothing once the region exists. A changed grant is refused (409), because the
  # region keeps the grant it was built with.
  update_path    = "/SetRegionGrant"
  update_method  = "POST"
  destroy_path   = "/DeleteRegion"
  destroy_method = "POST"
  destroy_data   = jsonencode({ regionId = restapi_object.reservation[0].id })

  ignore_all_server_changes = true
  # As teardown's, so that a destroy run again after the region is gone can finish.
  ignore_server_additions = true

  # Destroyed first, and kept until the region is gone, so the grant outlives the teardown.
  depends_on = [restapi_object.teardown]
}
