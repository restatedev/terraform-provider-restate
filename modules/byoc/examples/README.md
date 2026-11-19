# Examples

Each directory here is a small Terraform configuration that calls one of the modules in this repository with one way of
setting up access. Copy a directory, set its variables, and apply it. The [setup guide](../README.md) explains what each
step does in Restate Cloud and in your cloud account.

## Methods

- **`region`** creates one region and its grant in a single apply. The module creates the region in Restate Cloud and
  the grant in your cloud account, and Restate starts building the region. Use it for a region that stands alone.
- **`region-grant`** creates a grant that serves every region built in the cloud account, and builds the regions it
  lists. A new region is one more entry in the list, or a `restate_cloud_byoc_region` elsewhere with the grant's id,
  and needs no change in the cloud account. It uses the `restatedev/restate` provider for its Restate calls.

The `region` examples set `region_identity = false`, because Restate Cloud in production does not give new regions their
own identity yet. Set it to `true` where it does.

## Directories

- **AWS:** [`aws/region`](aws/region), [`aws/region-grant`](aws/region-grant) and
  [`aws/oidc-provider`](aws/oidc-provider). A region grant needs the account's OIDC provider for Restate's issuer.
  `aws/oidc-provider` creates it in a Terraform state of its own, because every grant in the account trusts it. Pass its
  `arn` output to `aws/region-grant` as `oidc_provider_arn`.
- **Google Cloud:** [`gcp/region`](gcp/region) and [`gcp/region-grant`](gcp/region-grant).
  In `gcp/region`, the optional `identity_project` puts the service account and its pool in a second project. The
  service account then cannot change its own access in the region project.
- **Azure:** [`azure/region`](azure/region) and [`azure/region-grant`](azure/region-grant).

The [AWS](../aws/README.md), [Google Cloud](../gcp/README.md) and [Azure](../azure/README.md) READMEs describe every input
and the permissions each grant holds.

## Running an example

Run Terraform with credentials for the cloud account that the region or grant is built in. Give the Restate Cloud
credential in an environment variable, so that it is never written to a file. For a `region` example, set either
`TF_VAR_restate_access_token`, an administrator's token from `restate cloud login`, or `TF_VAR_restate_api_key`, never
both. A `region-grant` example configures the `restate` provider, which reads `RESTATE_CLOUD_ACCOUNT_ID` and
`RESTATE_CLOUD_MANAGEMENT_API_KEY`.

```bash
cd examples/aws/region
cat > example.auto.tfvars <<'EOF'
restate_account_id = "acc_1a2b3c4d5e"
EOF
export TF_VAR_restate_access_token="$(yq -p toml -r '.global.cloud.access_token' "$HOME/.config/restate/config.toml")"
terraform init
terraform apply
```

This repository ignores `*.tfvars` files and Terraform state, so your values and the state stay out of git. Keep the
state, because you need it to remove what the example created. To use an example in a repository of your own, copy its
directory and change the module's `source` before you run `terraform init`. The new source is
`github.com/restatedev/terraform-provider-restate//modules/byoc/aws`, with the directory of your cloud in place of `aws`.

## Removing what an example created

Run `terraform destroy` in the same directory before you delete it or remove its module block. Each module configures its
providers itself, so Terraform needs the module block to remove what the module created.

- For a region, delete its environments first, because Restate Cloud refuses to delete a region that still has
  environments.
- For a region grant, delete the regions built with it first. Restate Cloud answers 409 while a region uses the grant,
  and the destroy stops with the grant still in place.
- Keep `aws/oidc-provider` while any grant in the account uses the provider. Its module refuses a destroy, and the
  comment at the top of `aws/oidc-provider/main.tf` says how to remove the provider on purpose.
