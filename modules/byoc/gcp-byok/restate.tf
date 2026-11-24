# Registers the region with Restate Cloud and installs the BYOC chart.
#
# The apply runs in this order:
#
# 1. main.tf and identity.tf create the cluster, the node pools, the identities and the bucket, and
#    point Config Connector at its service account.
# 2. restate_cloud_byok_registration_key asks Restate Cloud for a one-use key and the id of the region
#    it will register. A key expires four hours after it is created, so it is created last, after
#    everything that can take a long time, and the chart follows at once.
# 3. The registration Secret and the chart's release. The chart's region installation operator presents
#    the key to Restate Cloud, which creates the region under the reserved id, and installs a release
#    that Restate Cloud chooses. In direct image mode, the pull Secret of the chart's pods is created
#    from the key's image_pull credential before the release.
#
# A destroy runs backwards. The chart goes first, then the Secrets, and then the key, which removes the
# region at Restate Cloud when deregister_region_on_destroy is set. Restate refuses while the region
# hosts environments, so the destroy stops there with the cluster still in place, and runs again once
# the environments are deleted.

locals {
  direct = var.image_source_mode == "direct"

  # In direct mode the registry is the proxy of the control plane's domain, which is how the chart
  # derives it from controlPlane.url too. The chart refuses a restate-operator or canary image that is
  # not under it, and does not fill them in, so the module names them.
  image_registry = local.direct ? "registry.${trimprefix(var.control_plane_url, "https://api.")}" : var.image_source_registry

  image_source = local.direct ? {
    mode           = "direct"
    pullSecretName = var.image_pull_secret_name
    } : {
    mode     = "mirror"
    registry = var.image_source_registry
  }

  # The environment pods may use this selector and these tolerations and no others. They are the label
  # and the taint of the environments node pool.
  scheduling = {
    nodeSelector = { (local.environments_label_key) = local.environments_label_value }
    tolerations = [{
      key      = local.environments_label_key
      operator = "Equal"
      value    = local.environments_label_value
      effect   = "NoSchedule"
    }]
  }

  chart_values = {
    imageSource  = local.image_source
    registration = { secretName = var.registration_secret_name }
    controlPlane = { url = var.control_plane_url }
    region = {
      id             = restate_cloud_byok_registration_key.this.region_id
      cloud          = "gcp"
      providerRegion = var.region
      # The chart creates one tunnel Service for each zone, which the installer waits to get an address
      # before it activates the region, and the region installation operator reports the names to the
      # control plane.
      availabilityZones = local.availability_zones
    }
    staffGate  = { enabled = var.staff_gate_enabled }
    hold       = var.hold
    scheduling = local.scheduling
    environments = {
      gcpServiceAccount = google_service_account.environments.email
      snapshotBucket    = google_storage_bucket.snapshots.name
    }
    restateOperator = merge({
      enabled = true
      # The chart refuses any other repository, because its image policy admits only images under the
      # image source.
      image       = { repository = "${local.image_registry}/restate-operator" }
      canaryImage = "${local.image_registry}/alpine:${var.restate_operator_canary_image_tag}"
      # restate-operator writes a Config Connector IAMPolicyMember for the `restate` ServiceAccount of
      # each environment, which identity.tf lets Config Connector apply.
      gcpWorkloadIdentity = true
      },
      # A subchart cannot read the parent's values, so restate-operator names the pull Secret again.
      local.direct ? { imagePullSecrets = [{ name = var.image_pull_secret_name }] } : {},
    )
  }
}

resource "restate_cloud_byok_registration_key" "this" {
  cloud                        = "gcp"
  provider_region              = var.region
  region_display_name          = var.restate_region_display_name
  deregister_region_on_destroy = var.deregister_region_on_destroy

  # Created after the cluster is ready for the chart, and destroyed before any of it, so a refusal to
  # remove a region that hosts environments leaves the cluster in place.
  depends_on = [
    google_container_node_pool.system,
    google_container_node_pool.environments,
    google_storage_bucket_iam_member.environments,
    google_service_account_iam_member.config_connector_environments,
    helm_release.config_connector,
  ]
}

resource "kubernetes_namespace_v1" "release" {
  metadata {
    name = var.namespace
  }

  depends_on = [google_container_node_pool.system]
}

resource "kubernetes_secret_v1" "registration" {
  metadata {
    name      = var.registration_secret_name
    namespace = kubernetes_namespace_v1.release.metadata[0].name
  }

  data = {
    key = restate_cloud_byok_registration_key.this.key
  }
}

# The registry credential of the chart's pods in direct mode, from the pull key that comes with the
# registration key. Once the region is active, the region installation operator replaces the content
# with the region's own key, and labels and annotates the Secret with that key's id. All three are
# ignored after creation, or every apply would put the first key back or strip what the operator
# wrote. A new registration key reserves a new region, so the Secret is replaced with it.
resource "kubernetes_secret_v1" "image_pull" {
  count = local.direct ? 1 : 0

  metadata {
    name      = var.image_pull_secret_name
    namespace = kubernetes_namespace_v1.release.metadata[0].name
  }

  type = "kubernetes.io/dockerconfigjson"

  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        (restate_cloud_byok_registration_key.this.image_pull.registry) = {
          username = restate_cloud_byok_registration_key.this.image_pull.username
          password = restate_cloud_byok_registration_key.this.image_pull.password
          auth     = base64encode("${restate_cloud_byok_registration_key.this.image_pull.username}:${restate_cloud_byok_registration_key.this.image_pull.password}")
        }
      }
    })
  }

  lifecycle {
    ignore_changes       = [data, binary_data, metadata[0].annotations, metadata[0].labels]
    replace_triggered_by = [restate_cloud_byok_registration_key.this.region_id]

    precondition {
      condition     = restate_cloud_byok_registration_key.this.image_pull != null
      error_message = "Restate Cloud returned no registry credential with the registration key, so direct image mode cannot pull the region's images. The control plane serves no images, or predates the credential. Set image_source_mode to mirror."
    }

    precondition {
      condition     = try(restate_cloud_byok_registration_key.this.image_pull.registry, local.image_registry) == local.image_registry
      error_message = "The registration key's pull credential is for ${try(restate_cloud_byok_registration_key.this.image_pull.registry, "")}, but the chart pulls from ${local.image_registry}, which follows from control_plane_url. Check that control_plane_url is the control plane that issued the key."
    }
  }
}

resource "helm_release" "byoc" {
  name       = var.release_name
  namespace  = kubernetes_namespace_v1.release.metadata[0].name
  chart      = var.byoc_chart.chart
  repository = var.byoc_chart.repository
  version    = var.byoc_chart.version

  dependency_update = var.byoc_chart.dependency_update

  # The apply does not wait for the chart's pods. The Twingate Connector's pod starts only after the
  # installer has registered the region and written the Connector's token pair, so a wait would make
  # the apply depend on the registration. A wait that fails also leaves a release that a later apply
  # may uninstall and install again, which replaces the break-glass token with one Restate never saw.
  # Check the region as the README describes instead.
  wait = false

  lifecycle {
    precondition {
      condition     = length(local.availability_zones) >= 3 && length(local.availability_zones) <= 9
      error_message = "The BYOC chart needs 3 to 9 availability zones, and the cluster in ${var.region} runs in ${length(local.availability_zones)}. Set node_locations to a list of 3 to 9 zones."
    }
  }

  values = concat([yamlencode(local.chart_values)], var.chart_values)

  depends_on = [
    kubernetes_secret_v1.registration,
    kubernetes_secret_v1.image_pull,
    google_container_node_pool.environments,
  ]
}
