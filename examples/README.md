# Examples

These configurations are runnable and are embedded verbatim into the generated documentation, so they
are kept `terraform fmt` clean and are checked in CI.

```text
examples/
├── provider/provider.tf                  # the provider block, both credential sets
├── resources/<name>/resource.tf          # rendered into docs/resources/<name>.md
├── resources/<name>/import.sh            # the import command, rendered into the same page
├── data-sources/<name>/data-source.tf    # rendered into docs/data-sources/<name>.md
└── two-stage-bootstrap/                  # a complete, applyable pair of root modules
```

The first three layouts are what `terraform-plugin-docs` expects; those file names are not arbitrary.

## Start here: `two-stage-bootstrap/`

If you want something you can actually apply, use that. It is two root modules — one that provisions
an environment and its API key, one that configures the environment — with variables, outputs, and
teardown instructions. It is the only example that is complete on its own.

Everything else is a **documentation fragment**: single-resource snippets that get embedded into the
generated reference. Several reference variables a real configuration would supply, and the Admin API
ones assume a provider already configured with `admin_url` and `environment_api_key`. They are checked
for formatting and validity in CI, but they are not root modules.

## Trying a fragment

Copy it next to a provider block and supply the variables:

```sh
mkdir -p /tmp/restate-example && cd /tmp/restate-example
cp /path/to/examples/provider/provider.tf .
cp /path/to/examples/resources/restate_cloud_environment/resource.tf .

export RESTATE_CLOUD_ACCOUNT_ID=acc_...
export RESTATE_CLOUD_MANAGEMENT_API_KEY=...

terraform init
terraform plan
```

Point this at a throwaway account. Several examples set `deletion_protection = true` or leave
`force_destroy = false`, so tearing the result down takes two applies — deliberately. The destructive
operations guide explains each guard.

## Why the Admin API examples need a second stage

Terraform configures a provider before it plans any resource, so an `admin_url` produced by
`restate_cloud_environment` is not known when the provider that needs it is configured. Deployments,
Kafka clusters, subscriptions and service configuration therefore belong to a separate apply. See the
two-stage bootstrap guide, or `two-stage-bootstrap/` for the working version.
