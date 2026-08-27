# Read an environment this configuration does not manage -- typically stage 2 of a two-stage
# bootstrap, where the environment was created by a different state.

data "restate_cloud_environment" "production" {
  id = var.environment_id
}

# A data source's result is known at plan time, so it can configure a provider. The API key cannot
# come from here: Restate Cloud returns a key's secret only when the key is created.
provider "restate" {
  alias = "production"

  admin_url           = data.restate_cloud_environment.production.admin_base_url
  environment_api_key = var.restate_environment_api_key
}
