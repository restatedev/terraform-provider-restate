output "region_id" {
  description = "The id of the region, which Restate Cloud reserved with the registration key. The region exists once the chart has registered, and then `restate_cloud_environment` can create environments in it with this id."
  value       = restate_cloud_byok_registration_key.this.region_id
}

output "registration_key_expires_at" {
  description = "When the registration key stops working if the chart has not registered with it."
  value       = restate_cloud_byok_registration_key.this.expires_at
}

output "image_registry" {
  description = "The registry every Restate image in the cluster comes from: `registry.<domain>` in direct mode, `image_source_registry` in mirror mode."
  value       = local.image_registry
}

output "cluster_name" {
  description = "The name of the GKE cluster."
  value       = google_container_cluster.this.name
}

output "cluster_location" {
  description = "The location of the GKE cluster, which is the region."
  value       = google_container_cluster.this.location
}

output "cluster_endpoint" {
  description = "The address of the cluster's API server."
  value       = google_container_cluster.this.endpoint
}

output "namespace" {
  description = "The namespace of the chart's release."
  value       = kubernetes_namespace_v1.release.metadata[0].name
}

output "snapshot_bucket" {
  description = "The bucket for environments' snapshots, which the chart takes as `environments.snapshotBucket`."
  value       = google_storage_bucket.snapshots.name
}

output "environments_service_account" {
  description = "The Google service account the environments act as, which the chart takes as `environments.gcpServiceAccount`."
  value       = google_service_account.environments.email
}

output "config_connector_service_account" {
  description = "The Google service account Config Connector acts as. It can change the IAM policy of the environments' service account and of nothing else."
  value       = google_service_account.config_connector.email
}

output "node_service_account" {
  description = "The Google service account the nodes run as."
  value       = google_service_account.nodes.email
}

output "scheduling" {
  description = "The node selector and tolerations that the chart lets environment pods use, which match the environments node pool."
  value       = local.scheduling
}
