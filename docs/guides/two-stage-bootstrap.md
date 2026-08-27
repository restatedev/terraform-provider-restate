---
page_title: "Two-stage bootstrap: provisioning an environment and then configuring it"
subcategory: "Guides"
description: |-
  Why provisioning a Restate Cloud environment and configuring it cannot happen in one apply, and
  how to structure the two stages so credentials never pass through a plan file.
---

# Two-stage bootstrap

This provider talks to two APIs. The Cloud Management API creates environments and environment API
keys. Each environment then has its own Admin API, which registers deployments and manages Kafka
clusters, subscriptions, and service configuration.

The second one needs a URL and a credential that the first one produces. That is where people get
stuck, because the obvious configuration does not work:

```hcl
# This does NOT work.
resource "restate_cloud_environment" "production" {
  name = "production"
}

resource "restate_cloud_api_key" "terraform" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::AdminAccess"
}

provider "restate" {
  alias               = "production"
  admin_url           = restate_cloud_environment.production.admin_base_url # unknown at plan time
  environment_api_key = restate_cloud_api_key.terraform.api_key             # unknown at plan time
}
```

## Why it cannot work

Terraform configures a provider **before** it plans or applies any resource that uses it. A
provider's configuration therefore has to be known during planning. On the first run, the
environment does not exist yet, so `admin_base_url` and `api_key` are unknown, and there is nothing
Terraform can hand the provider.

The provider detects this and fails with an explanation rather than sending a placeholder as a
bearer token:

```
│ Error: Provider configuration is not known yet
│
│ The provider attributes `admin_url`, `environment_api_key` depend on values that are not known
│ until apply. Terraform cannot configure a provider from them.
```

This is not a limitation of this provider. It applies to every Terraform provider whose
configuration comes from a resource in the same configuration, and it is why the AWS provider cannot
be configured from an EKS cluster it creates in the same apply either.

## The two stages

Split the work into two states, and pass the connection details between them deliberately.

A complete, applyable version of everything below — with variables, outputs and teardown
instructions — is in [`examples/two-stage-bootstrap/`](https://github.com/restatedev/terraform-provider-restate/tree/main/examples/two-stage-bootstrap)
in the provider repository.

### Stage 1: provision

```hcl
terraform {
  required_providers {
    restate = { source = "restatedev/restate" }
  }
}

provider "restate" {
  account_id         = var.restate_cloud_account_id
  management_api_key = var.restate_cloud_management_api_key
}

resource "restate_cloud_environment" "production" {
  name    = "production"
  region  = "us-east-1"
  tier_id = "BUSINESS"

  # Guard a real environment. Destroying one is irreversible and takes its data with it.
  deletion_protection = true
}

resource "restate_cloud_api_key" "terraform" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::AdminAccess"
  description    = "terraform stage 2"
}
```

Now export the two values stage 2 needs. **Do not export the key as a Terraform output** unless
stage 2 reads stage 1's state directly and that state is encrypted and access-controlled: an output
is stored in plaintext in the state file and is readable by anyone who can read that state.

Prefer writing the secret to a secret store, so stage 2 reads it from there:

```hcl
# Example using AWS Secrets Manager. Substitute your own secret store.
resource "aws_secretsmanager_secret" "restate_admin" {
  name = "restate/production/admin"
}

resource "aws_secretsmanager_secret_version" "restate_admin" {
  secret_id = aws_secretsmanager_secret.restate_admin.id
  secret_string = jsonencode({
    admin_url = restate_cloud_environment.production.admin_base_url
    api_key   = restate_cloud_api_key.terraform.api_key
  })
}

# The admin URL is not secret, so a plain output is fine for it.
output "admin_url" {
  value = restate_cloud_environment.production.admin_base_url
}
```

### Stage 2: configure

A separate state, applied after stage 1:

```hcl
data "aws_secretsmanager_secret_version" "restate_admin" {
  secret_id = "restate/production/admin"
}

locals {
  restate = jsondecode(data.aws_secretsmanager_secret_version.restate_admin.secret_string)
}

provider "restate" {
  admin_url           = local.restate.admin_url
  environment_api_key = local.restate.api_key
}

# Admin API resources go here: deployments, Kafka clusters, subscriptions, service configuration.
```

A `data` source *is* known at plan time, so this configures cleanly. The same shape works with
Vault, GCP Secret Manager, SOPS, or environment variables set by your CI system —
`RESTATE_ADMIN_URL` and `RESTATE_AUTH_TOKEN`.

## If you would rather not use a secret store

Two alternatives, both worse in a specific way:

**Read stage 1's state with `terraform_remote_state`.** Convenient, but it means stage 2 can read
everything in stage 1's state, and it makes stage 1's state a distribution channel for a live
credential. Only do this if both states are already equally protected.

**Create the key outside Terraform.** Provision the environment with Terraform, mint an
`AdminAccess` key by hand or in a script, and give stage 2 the key through `RESTATE_AUTH_TOKEN`.
The key is then not managed by Terraform, which means nothing rotates or revokes it for you.

## What about a single apply?

You can run two applies against two configurations from one CI job, which gets you a single
*pipeline* even though it is two Terraform runs:

```sh
terraform -chdir=stage1 apply -auto-approve
terraform -chdir=stage2 apply -auto-approve
```

There is no supported way to collapse this into one apply. A provider that took its credentials
per-resource instead of per-provider could do it, but every Admin resource would then have to carry
a copy of the environment credential in its own state, multiplying the number of places a live
credential is stored. That trade is not made by default.

## Importing an environment someone else created

If the environment already exists, you do not need stage 1 at all. Either import it:

```sh
terraform import restate_cloud_environment.production acc_1a2b3c/env_1a2b3c
```

or read it without managing it:

```hcl
data "restate_cloud_environment" "production" {
  id = "env_1a2b3c"
}

output "admin_url" {
  value = data.restate_cloud_environment.production.admin_base_url
}
```

An imported or looked-up environment still cannot configure a provider in the same apply if the
lookup and the provider are in the same configuration — but a `data` source's result *is* known at
plan time, so this **does** work:

```hcl
data "restate_cloud_environment" "production" {
  id = var.environment_id
}

provider "restate" {
  alias = "production"

  admin_url = data.restate_cloud_environment.production.admin_base_url

  # Still needs a key from somewhere: an API key's secret cannot be read back, only created.
  environment_api_key = var.restate_environment_api_key
}
```

The remaining constraint is the key, not the URL. Restate Cloud returns an API key's secret only
when the key is created, so no data source and no import can produce it.

## Secrets in state, summarised

| Value | Where it lives | Notes |
| --- | --- | --- |
| `management_api_key` | Provider configuration only | Never written to resource state. |
| `environment_api_key` | Provider configuration only | Never written to resource state. |
| `restate_cloud_api_key.api_key` | **Terraform state**, marked sensitive | The only copy. Cannot be read back or re-imported. |
| `restate_kafka_cluster.properties` | **Terraform state**, marked sensitive | Can contain SASL passwords; the API redacts them on read. |

Terraform state containing a live credential is not a provider quirk, it is how Terraform works. Use
an encrypted remote backend, restrict who can read it, and never commit state or a `.tfvars` holding
these values.
