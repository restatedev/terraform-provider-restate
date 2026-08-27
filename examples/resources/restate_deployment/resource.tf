# Registering a deployment performs service discovery: Restate calls the endpoint to learn which
# services and handlers it exposes. That call goes to your own infrastructure, so give it a timeout
# that matches how long your endpoint takes to become reachable and respond.

resource "restate_deployment" "orders" {
  http = {
    uri = "https://orders.internal.example.com:9080"

    # Sensitive. Preserved from configuration on refresh, so a value the API declines to echo back
    # cannot silently erase the configured credential.
    additional_headers = {
      "x-api-key" = var.orders_api_key
    }

    # Changing metadata replaces the deployment: the Admin API's update request cannot accept it.
    metadata = {
      team = "orders"
    }
  }

  # Deleting a deployment can leave in-flight invocations unrecoverable, and forced deletion is the
  # only mode the Admin API implements. Terraform refuses to destroy this resource until the flag is
  # set and applied.
  force_destroy = false

  timeouts {
    create = "10m"
    update = "10m"
  }
}

# A Cloud Run function that authenticates callers with a Google-signed OIDC token.
resource "restate_deployment" "billing" {
  http = {
    uri = "https://billing-abc123-uc.a.run.app"

    auth = {
      google_id_token = {
        impersonate_service_account = "restate-invoker@my-project.iam.gserviceaccount.com"
      }
    }
  }
}

# An AWS Lambda deployment, invoked through a chained role.
resource "restate_deployment" "reports" {
  lambda = {
    arn             = "arn:aws:lambda:us-east-1:123456789012:function:reports:3"
    assume_role_arn = "arn:aws:iam::123456789012:role/restate-invoke"
  }
}

output "discovered_services" {
  value = restate_deployment.orders.services
}

variable "orders_api_key" {
  type      = string
  sensitive = true
}
