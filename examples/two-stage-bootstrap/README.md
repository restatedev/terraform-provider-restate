# Two-stage bootstrap: a complete, runnable example

This is the configuration the two-stage bootstrap guide describes, as two Terraform root modules you
can actually apply.

Provisioning an environment and configuring it cannot happen in one apply. Terraform configures a
provider *before* it plans any resource, so an `admin_url` and API key produced by
`restate_cloud_environment` and `restate_cloud_api_key` are not known when the provider that needs
them is configured. The provider detects this and fails with an explanation rather than sending a
placeholder as a bearer token.

```text
stage1-provision/    creates the environment and an AdminAccess key
stage2-configure/    registers deployments and Kafka objects in that environment
```

## Running it

Stage 1 needs an account-scoped management key. Mint one outside Terraform; this provider cannot
create it, because account-scoped operations require an interactive user identity.

```sh
export RESTATE_CLOUD_ACCOUNT_ID=acc_...
export RESTATE_CLOUD_MANAGEMENT_API_KEY=...       # rst:role::ManagementApiAccess

terraform -chdir=stage1-provision init
terraform -chdir=stage1-provision apply
```

Stage 2 needs the environment's admin URL and an environment-scoped key. **How you move them between
stages is a security decision**, and this example takes the least-bad option that needs no extra
infrastructure: read them from stage 1's state and pass them as environment variables, so they are
never written into stage 2's configuration.

```sh
export RESTATE_ADMIN_URL=$(terraform -chdir=stage1-provision output -raw admin_url)
export RESTATE_AUTH_TOKEN=$(terraform -chdir=stage1-provision output -raw admin_api_key)

terraform -chdir=stage2-configure init
terraform -chdir=stage2-configure apply
```

For anything beyond a trial, put the key in a secret store instead and have stage 2 read it with a
`data` source — a data source's result *is* known at plan time, so it configures a provider cleanly.
The guide shows that shape. Reading stage 1's state directly means anyone who can read that state can
read the credential.

## Tearing it down

Stage 2 first, then stage 1. Stage 1 sets `deletion_protection = true`, so destroying it takes two
applies — deliberately:

```sh
terraform -chdir=stage2-configure destroy

terraform -chdir=stage1-provision apply -var unprotect=true
terraform -chdir=stage1-provision destroy
```

## What this example does not do

- It does not manage the account, its billing, or the management key. Those need an interactive user
  identity.
- It does not rotate the environment key. Rotation is
  `terraform apply -replace=restate_cloud_api_key.terraform`, which mints a new secret and revokes the
  old one; stage 2 then needs the new value.
