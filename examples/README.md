# Examples

These configurations are runnable and are embedded verbatim into the generated documentation, so they
are kept `terraform fmt` clean and are checked in CI.

```text
examples/
├── provider/provider.tf                       # the provider block, both credential sets
├── resources/<name>/resource.tf                # example usage, rendered into docs/resources/<name>.md
├── resources/<name>/import.sh                  # the import command, rendered into the same page
└── data-sources/<name>/data-source.tf          # example usage, rendered into docs/data-sources/
```

The layout is what `terraform-plugin-docs` expects; the file names are not arbitrary.

## Running one

Each directory is a fragment, not a standalone root module: several reference variables or resources
that a real configuration would supply. To try one, copy it into a directory alongside a provider
block and supply the variables:

```sh
mkdir -p /tmp/restate-example && cd /tmp/restate-example
cp /path/to/examples/provider/provider.tf .
cp /path/to/examples/resources/restate_cloud_environment/resource.tf .

export RESTATE_CLOUD_ACCOUNT_ID=acc_...
export RESTATE_CLOUD_MANAGEMENT_API_KEY=...

terraform init
terraform plan
```

Point this at a throwaway account. Several examples set `deletion_protection = true`, which means
tearing the result down takes two applies — that is deliberate, and is explained in the destructive
operations guide.

## Two stages, not one

The Admin API examples — deployments, Kafka clusters, subscriptions, service configuration — need a
provider configured with `admin_url` and `environment_api_key`. Those cannot come from a
`restate_cloud_environment` in the same apply. See the two-stage bootstrap guide before wiring them
together.
