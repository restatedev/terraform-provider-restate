# Terraform Provider for Restate Cloud

A Terraform provider that provisions [Restate Cloud](https://restate.dev) environments through the
Cloud Management API and configures them through the per-environment
[Admin API](https://docs.restate.dev/admin-api/deployment/list-deployments).

> **Status:** under active development. See [AGENTS.md](AGENTS.md) for the development plan, the
> phase checklist, and the decisions that are still open.

## What it manages

| Resource | Contract | Purpose |
| --- | --- | --- |
| `restate_cloud_environment` | Cloud Management API | Create, rename, resize, import and destroy environments |
| `restate_cloud_api_key` | Cloud Management API | Environment-scoped API keys |
| `restate_cloud_region_grant` | Cloud Management API | Managed BYOC region grants of an account or an organization |
| `restate_cloud_region_grant_confirmation` | Cloud Management API | Confirming a region grant with the cloud side the grant module creates |
| `restate_cloud_byoc_region` | Cloud Management API | Managed BYOC regions built with a region grant |
| `restate_deployment` | Admin API | Register HTTP and AWS Lambda service deployments |
| `restate_kafka_cluster` | Admin API | Kafka cluster connection configuration |
| `restate_subscription` | Admin API | Kafka-to-handler subscriptions |
| `restate_service_configuration` | Admin API | Stable per-service configuration overrides |

The grant modules for Managed BYOC live in [`modules/byoc`](modules/byoc), one for each cloud. They create the cloud
side of a region grant and use this provider's resources for the Restate side, so one configuration creates the grant,
confirms it and builds its regions.

Data sources cover environment, region, region grant and account lookups on the Cloud side, and server info,
service, handler, deployment, Kafka cluster and subscription lookups on the Admin side.

The provider does **not** manage Restate Cloud accounts, account-scoped management API keys,
billing, members, groups or role bindings. Those operations require an interactive user identity.

## Two API contracts, two credentials

The provider talks to two independent APIs and deliberately keeps their credentials separate:

```hcl
provider "restate" {
  # Cloud Management API — provisions environments and environment API keys.
  cloud_api_url      = "https://api.us.restate.cloud/cloud"
  account_id         = var.restate_cloud_account_id
  management_api_key = var.restate_cloud_management_api_key # rst:role::ManagementApiAccess

  # Admin API — configures one existing environment.
  admin_url           = var.restate_admin_url
  environment_api_key = var.restate_environment_api_key
}
```

A management key is never sent to an environment Admin API, and an environment key is never sent
to the Cloud Management API. Cloud-only, Admin-only, and combined configurations are all valid.

Because Terraform must be able to configure a provider before any resource exists, provisioning an
environment and configuring it are **two stages**. See `docs/guides/two-stage-bootstrap.md`.

## Development

```sh
go build ./...
go test ./...
go generate ./pkg/api/...   # regenerates both API clients; CI fails on a diff
```

## Security

Terraform state contains every secret this provider stores, including environment API keys, Kafka
connection properties, and deployment headers. Encrypt and access-control your remote state; see
`templates/guides/state-and-secrets.md` for exactly which values go where, and
`templates/guides/destructive-operations.md` for every operation that can lose data and the guard
around it. Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).

## Status by phase

| Phase | Scope | State |
| --- | --- | --- |
| 0 | Contract survey, vendored snapshots, generator, domain types | Complete except live lifecycle probes |
| 1 | Generated clients, API facades, provider configuration, `restate_server_info` | Complete |
| 2 | `restate_cloud_environment`, `restate_cloud_api_key`, Cloud data sources, bootstrap guide | Complete except live acceptance runs |
| 3 | `restate_deployment` | Complete except live acceptance runs |
| 4 | `restate_kafka_cluster`, `restate_subscription` | Complete except live acceptance runs |
| 5 | Service lookups and `restate_service_configuration` | Complete; `restate_service_configuration` ships as an explicitly partial-management resource |
| 6 | Release readiness | CI, docs, examples, and signed releases in place; version matrix and prerelease outstanding |

Every unresolved question is recorded under "Decisions still requiring validation" in
[AGENTS.md](AGENTS.md). Nothing there has been silently guessed at: where live behaviour is unknown,
the provider either refuses the operation with an explanation or holds the attribute back.

## Testing

Three layers, in increasing cost:

```sh
go test ./pkg/...       # client contract tests against httptest fixtures
go test ./internal/...  # Terraform protocol tests; downloads Terraform on first run
```

Documentation under `docs/` is generated from the schema descriptions and the runnable
configurations in `examples/`. Handwritten guides live in `templates/guides/`.

```sh
go generate ./pkg/api/...   # regenerates both API clients
go generate ./tools/...     # regenerates docs/
```

On macOS use a short `TMPDIR` for docs generation (`TMPDIR=/tmp/tfdocs go generate ./tools/...`):
generation launches the provider over a Unix domain socket, and macOS caps a socket path at 104
bytes.

Acceptance tests run against real APIs and are gated so they never run implicitly:

```sh
# Cloud Management API, in a designated test account only.
TF_ACC=1 \
RESTATE_CLOUD_ACCOUNT_ID=acc_... \
RESTATE_CLOUD_MANAGEMENT_API_KEY=... \
go test ./internal/provider/ -run TestAcc -v -timeout 60m

# Admin API, against a designated test environment.
TF_ACC=1 \
RESTATE_ADMIN_URL=https://....restate.cloud:9070 \
RESTATE_AUTH_TOKEN=... \
go test ./internal/provider/ -run TestAcc -v -timeout 60m
```

Acceptance tests create environments and delete deployments, so they are gated twice: the
credentials must be present, **and** `RESTATE_ACC_ALLOW_DESTRUCTIVE=1` must be set for anything that
creates or deletes a real object. Never point them at an account or environment you care about.
