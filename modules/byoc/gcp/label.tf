# An older region binds its grant to the region through a project label whose value is the region
# tag, under a key starting restate-cloud-region. The composition builds nothing until it reads it.
# The module does not own the project, so it adds a key of the region's own through Cloud Resource
# Manager and keeps every other label. For now it also sets the plain restate-cloud-region key, which
# replaces a tag an earlier region left there (see own_labels). A region with
# its own identity has no tag, and the labels are left as they are.
#
# Destroyed after the region (restapi_object.region depends on it), because the composition needs
# the label to delete what it built.

data "google_client_config" "current" {}

data "google_project" "region" {
  count      = local.labelled ? 1 : 0
  project_id = var.project
}

provider "restapi" {
  alias                = "gcp"
  uri                  = "https://cloudresourcemanager.googleapis.com/v3"
  bearer_token         = data.google_client_config.current.access_token
  write_returns_object = false
  headers              = { "content-type" = "application/json" }
}

locals {
  # Known when planning, unlike the tag, which a new region only learns from Restate Cloud.
  labelled  = var.restate != null || var.region_tag != null
  label_key = local.region_tag == null ? null : "restate-cloud-region-${local.region_tag}"
  # TODO(region-tag-any-key): drop the plain restate-cloud-region key once every composition
  # cluster runs a region release whose restate.grantBound accepts any restate-cloud-region* key.
  # Until then it is the only key an older release reads.
  own_labels = local.region_tag == null ? {} : { (local.label_key) = local.region_tag, "restate-cloud-region" = local.region_tag }
  # Both keys go on destroy whatever they hold: the project serves one region, and a value read
  # before this apply would put back the tag it replaces.
  other_labels = { for k, v in try(data.google_project.region[0].effective_labels, {}) : k => v if !contains(keys(local.own_labels), k) }
  labels       = merge(local.other_labels, local.own_labels)
}

# The PATCH answers with an Operation, not the project, so the provider reads the project back.
# updateMask=labels replaces the whole map, and the labels were read when planning, so a label
# added between plan and apply is lost. Cloud Resource Manager has no mask for a single key.
resource "restapi_object" "region_label" {
  count    = local.labelled ? 1 : 0
  provider = restapi.gcp

  object_id      = var.project
  path           = "/projects"
  read_path      = "/projects/{id}"
  create_path    = "/projects/{id}?updateMask=labels"
  create_method  = "PATCH"
  data           = jsonencode({ labels = local.labels })
  update_path    = "/projects/{id}?updateMask=labels"
  update_method  = "PATCH"
  destroy_path   = "/projects/{id}?updateMask=labels"
  destroy_method = "PATCH"
  destroy_data   = jsonencode({ labels = local.other_labels })

  # The provider keeps the project as read back in data. Ignoring every server change would also
  # hide a change to the labels, so only the project's other fields are ignored.
  ignore_server_additions = true
}
