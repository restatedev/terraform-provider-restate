# The identities and the storage of the region, in the same shape as Managed BYOC builds them for a
# Google Cloud region (byoc-infra's environment-identity, config-connector-identity and
# node-service-account templates in restatedev/restate-cloud):
#
# - The nodes run as a service account of their own, instead of the project's default Compute Engine
#   account, which holds Editor on the project unless an organization policy removes it.
# - The environments act as one Google service account, which reaches the snapshot bucket and nothing
#   else. Workload identity binds one Kubernetes ServiceAccount at a time by namespace, and the
#   namespaces are the environments the control plane creates later. The binding is therefore made in
#   the cluster: for each environment, restate-operator writes a Config Connector IAMPolicyMember that
#   lets the environment's `restate` ServiceAccount act as the environments' service account.
# - Config Connector acts as a service account that may change the IAM policy of the environments'
#   service account and of nothing else, so a compromised operator cannot grant anything else in the
#   project.
#
# GKE gives the same identity to a namespace and a ServiceAccount in every cluster of the project, so
# keep the `env-*` namespaces out of other clusters in the project.

locals {
  environments_account_id     = "${var.name}-env"
  config_connector_account_id = "${var.name}-cnrm"
  nodes_account_id            = "${var.name}-nodes"
  snapshot_bucket_name        = coalesce(var.snapshot_bucket_name, "${var.name}-snapshots-${var.project}")
}

resource "google_service_account" "nodes" {
  project      = var.project
  account_id   = local.nodes_account_id
  display_name = "GKE nodes of Restate region ${var.name}"

  depends_on = [google_project_service.this]
}

resource "google_project_iam_member" "nodes" {
  for_each = toset(var.node_service_account_roles)
  project  = var.project
  role     = each.value
  member   = google_service_account.nodes.member
}

resource "google_storage_bucket" "snapshots" {
  project  = var.project
  name     = local.snapshot_bucket_name
  location = upper(var.region)
  labels   = var.labels

  # Permissions come from IAM alone, with no per-object ACLs, which is what lets workload identity be
  # all an environment needs.
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = var.snapshot_bucket_force_destroy

  depends_on = [google_project_service.this]
}

resource "google_service_account" "environments" {
  project      = var.project
  account_id   = local.environments_account_id
  display_name = "Restate environments in region ${var.name}"

  depends_on = [google_project_service.this]
}

# On the bucket rather than the project, so the environments reach no other bucket of the project.
resource "google_storage_bucket_iam_member" "environments" {
  bucket = google_storage_bucket.snapshots.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.environments.member
}

resource "google_service_account" "config_connector" {
  project      = var.project
  account_id   = local.config_connector_account_id
  display_name = "Config Connector in Restate region ${var.name}"

  depends_on = [google_project_service.this]
}

# On the environments' service account only, so Config Connector can change its IAM policy and no
# other resource's.
resource "google_service_account_iam_member" "config_connector_environments" {
  service_account_id = google_service_account.environments.name
  role               = var.config_connector_environment_role
  member             = google_service_account.config_connector.member
}

# The add-on's controller acts as the service account above. The workload pool exists only once the
# cluster does.
resource "google_service_account_iam_member" "config_connector_workload_identity" {
  service_account_id = google_service_account.config_connector.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project}.svc.id.goog[cnrm-system/cnrm-controller-manager]"

  depends_on = [google_container_cluster.this]
}

# The add-on creates the ConfigConnector kind from a pod, so it appears some time after the first node.
resource "time_sleep" "config_connector_kind" {
  create_duration = var.config_connector_ready_delay

  depends_on = [
    google_container_node_pool.system,
    google_container_node_pool.environments,
  ]
}

# The add-on runs nothing until this object exists. Its name is fixed by Config Connector, which
# accepts only this one, and cluster mode gives the whole cluster one controller and one identity. A
# chart of its own applies it, because a Terraform manifest resource reads the kind's schema from the
# cluster while it plans, which cannot work in the apply that creates the cluster.
resource "helm_release" "config_connector" {
  name      = "config-connector"
  namespace = "kube-system"
  chart     = "${path.module}/charts/config-connector"
  wait      = true
  timeout   = var.helm_timeout_seconds

  values = [yamlencode({
    googleServiceAccount = google_service_account.config_connector.email
  })]

  depends_on = [
    time_sleep.config_connector_kind,
    google_service_account_iam_member.config_connector_workload_identity,
  ]
}
