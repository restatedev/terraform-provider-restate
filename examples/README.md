# Examples

```text
examples/
├── provider/                   the provider block, both credential sets
├── resources/<name>.tf         embedded into docs/resources/<name>.md
├── resources/<name>.sh         the import command, embedded into the same page
├── data-sources/<name>.tf      embedded into docs/data-sources/<name>.md
└── two-stage-bootstrap/        a complete, applyable pair of root modules
```

`resources/` and `data-sources/` are each **one Terraform module**, not a pile of unrelated
snippets. The files are named after the page that embeds them, and they reference each other the way
a real configuration would: `restate_subscription.tf` consumes the cluster that
`restate_kafka_cluster.tf` declares, and CI validates each directory as a whole. `variables.tf`
holds the values a real configuration would supply and is the only file not rendered anywhere.

The layout comes from `templates/resources.md.tmpl` and `templates/data-sources.md.tmpl`. Adding a
resource means adding `resources/<name>.tf` and `resources/<name>.sh`, or generation fails.

## The application

Every example describes the same system, so the snippets across the reference fit together instead
of each inventing its own names.

| | |
| --- | --- |
| environment | `production`, `us-east-1`, `BUSINESS` |
| API keys | `terraform` (`AdminAccess`), `ingress` (`IngressAccess`) |
| deployments | `orders` (HTTP), `billing` (Cloud Run), `reports` (Lambda) |
| services | `OrderService` with handler `handleOrder`, `OnboardingWorkflow` |
| Kafka | cluster `events`, topic `orders` |

## Applying it

`two-stage-bootstrap/` is the applyable version: one root module that provisions the environment and
its API key, one that configures the environment, with teardown instructions in its README.

The `resources/` module deliberately is not applyable in one run. It declares Cloud and Admin
resources together, and Terraform configures a provider before it plans any resource, so an
`admin_url` produced by `restate_cloud_environment` is not known when the provider that needs it is
configured. That split is what `two-stage-bootstrap/` exists to demonstrate.

Point any of this at a throwaway account. Several examples set `deletion_protection = true` or leave
`force_destroy = false`, so tearing the result down takes two applies — deliberately. The destructive
operations guide explains each guard.
