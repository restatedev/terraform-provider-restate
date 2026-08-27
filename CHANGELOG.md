# Changelog

All notable changes are recorded here. This provider has not been released yet; the entries below
describe what is on `main`.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions will follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) once released. Note that a refreshed
OpenAPI snapshot which changes or removes a field the provider reads is a **breaking change for the
provider**, and gets a minor or major release with upgrade notes, even when the API itself considers
the change compatible.

## Unreleased

### Added

Resources:

- `restate_cloud_environment` — create, rename, resize, import and destroy Restate Cloud
  environments. Creation and destruction poll to a terminal state; `deletion_protection` guards a
  destroy; a region change replaces the environment because the API has no migration operation.
- `restate_cloud_api_key` — environment-scoped API keys. The bearer secret is available only from the
  create response, so it is stored in state, marked sensitive, and cannot be recovered by an import.
  Every change replaces the key, because the API has no update operation.
- `restate_deployment` — register an HTTP or AWS Lambda service deployment. Registration performs
  service discovery, so timeouts are configurable. Deletion requires `force_destroy`: the API
  implements only forced deletion, which can break in-flight invocations.
- `restate_kafka_cluster` — Kafka connection configuration, with redaction-aware state handling so a
  refresh neither erases a credential the API will not disclose nor produces a permanent diff.
- `restate_subscription` — Kafka-to-handler subscriptions. Every change replaces the subscription,
  because the API has no update operation.
- `restate_service_configuration` — the mutable subset of a discovered service's configuration, as an
  explicitly **partial-management** resource: removing it, or removing an attribute from it, stops
  management and leaves the applied value in place, because the API has no reset operation.

Data sources:

- `restate_cloud_account`, `restate_cloud_environment`, `restate_cloud_regions` on the Cloud side.
- `restate_server_info`, `restate_service`, `restate_service_handler`, `restate_deployment`,
  `restate_kafka_cluster`, `restate_subscription` on the Admin side.

Provider:

- Dual configuration with strict credential separation: a management key is only ever sent to the
  Cloud Management API, and an environment key only to the configured Admin API. Cloud-only,
  Admin-only, combined, and alias-only configurations are all valid.
- Cloud credentials are validated with a read-only account read, and the Admin API's advertised
  version range is checked against the provider's supported range at configure time.
- Configuration that is not known until apply is refused with an explanation of the two-stage
  bootstrap, rather than sending a placeholder as a bearer token.

Documentation:

- Guides on the two-stage bootstrap, state and secret handling, and every destructive operation.
- Generated reference documentation and runnable examples for every resource and data source.

### Notes

- Restate Cloud API contract: `v1`, from `restatedev/restate-cloud` at `d9886ea`.
- Restate Admin API contract: `1.7.6`, from <https://docs.restate.dev/schemas/openapi-admin.json>.
- Supported Admin API versions: 2 to 4. The client was generated against version 4 (Restate 1.7.6);
  compatibility with a server whose maximum is 2 or 3 has not been tested and is not advertised.
