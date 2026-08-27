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
| `restate_deployment` | Admin API | Register HTTP and AWS Lambda service deployments |
| `restate_kafka_cluster` | Admin API | Kafka cluster connection configuration |
| `restate_subscription` | Admin API | Kafka-to-handler subscriptions |
| `restate_service_configuration` | Admin API | Stable per-service configuration overrides |

Data sources cover environment, region and account lookups on the Cloud side, and server info,
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

Terraform state contains every secret this provider reads back, including environment API keys and
Kafka connection properties. Encrypt and access-control your remote state. Report vulnerabilities
as described in [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).
