# The provider talks to two independent APIs with two separate credentials, and one provider block
# may carry either set or both. A management key is never sent to an environment, and an environment
# key is never sent to the Cloud Management API.

terraform {
  required_providers {
    restate = {
      source = "restatedev/restate"
    }
  }
}

# The Cloud Management API: provisions environments and environment API keys in one account.
# Both attributes fall back to RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY.
provider "restate" {
  account_id         = "acc_1a2b3c"
  management_api_key = var.restate_cloud_management_api_key
}

# One environment's Admin API: registers deployments and manages Kafka clusters, subscriptions, and
# service configuration. Both attributes fall back to RESTATE_ADMIN_URL and RESTATE_AUTH_TOKEN.
#
# These values come from an earlier apply. They cannot be references to a `restate_cloud_environment`
# in this configuration: Terraform configures a provider before it plans any resource, so the URL and
# key would still be unknown. See the two-stage bootstrap guide.
provider "restate" {
  alias = "production"

  admin_url           = "https://env_1a2b3c.env.us.restate.cloud:9070"
  environment_api_key = var.restate_environment_api_key
}
