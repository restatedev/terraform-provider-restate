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

Managed BYOC, from the Cloud Management API snapshot of 2026-11-12:

- `restate_cloud_region_grant` creates a region grant for one AWS account, GCP project or Azure
  subscription, owned by the account or by its organization (`owner = "ORGANIZATION"`). It exports
  the values the grant module in `modules/byoc` builds the cloud side from. A
  refresh that finds the grant expired or deleted removes it, so the next apply creates a new one.
- `restate_cloud_region_grant_confirmation` hands the module's `grant` output to Restate and waits
  until the grant is `READY`. Restate Cloud confirms a grant in the background, so the resource
  describes the grant until it is `READY` or reports `CONFIRMATION_FAILED`, and a timeout reports the
  cloud's last answer. The grant resource and data source export `status_reason`. Its destroy
  deletes the grant at Restate, which Terraform runs before it removes the cloud side, and which
  Restate refuses while a region still uses the grant. A changed output confirms the grant again in
  place instead of replacing it, because a replacement would delete the grant.
- `restate_cloud_byoc_region` builds a region with a `READY` grant, in the grant's cloud account, and
  polls until it is `ACTIVE`. Its destroy repeats `DeleteRegion` until the teardown has finished.
  `deletion_protection` guards a destroy.
- The `restate_cloud_region_grant` data source, for configurations that build regions with an
  organization's grant that another configuration manages.
- The Managed BYOC grant modules moved here from `restatedev/terraform-restate-byoc`, as
  `modules/byoc/aws` (with `aws/oidc-provider`), `modules/byoc/gcp` and `modules/byoc/azure`. Their
  region-grant mode now uses the resources above through the caller's `restate` provider instead of
  `restapi`, takes `region_grant = { owner, display_name, regions }`, and no longer takes
  `account_id`, `api_url`, `restate_api_key` or `restate_access_token` in that mode. The confirmation
  waits for Restate's background sign-in, instead of a fixed sleep. CI tests every module,
  its examples and the call-order harness against the provider built from the same commit.
  Region-scoped mode (`restate`) still uses `restapi`, because the provider has no resource for
  `SetRegionGrant` yet.
- `management_api_key` may be an organization API key with role
  `rst:role::OrganizationRegionGrantAccess`. The provider recognises it at configure time and gives
  an explicit error when it is used for anything other than region grants and BYOC regions.

BYOK, where the customer builds the Kubernetes cluster, from the Cloud Management API snapshot of
2026-11-20 (the `main` revision of `restatedev/restate-cloud` recorded in
`pkg/api/cloud/PROVENANCE.md`, which includes the merge of #2699):

- `restate_cloud_byok_registration_key` creates the one-use key that registers a BYOK region, and
  exports the key (sensitive), the region id it reserves and its expiry. A key expires four hours after
  it is created. The API cannot read a key back, so a refresh keeps the state, every argument other than
  `deregister_region_on_destroy` replaces the key, and the resource cannot be imported. With
  `deregister_region_on_destroy` a destroy also removes the region the key registered
  (`DeleteByokRegion`), which Restate refuses while the region hosts environments. It is off by default.
- `modules/byoc/gcp-byok` builds a GKE cluster for a BYOK region, registers it with the resource above and
  installs the BYOC Helm chart. It turns on `enable_insecure_binding_system_unauthenticated` for the
  Concierge and keeps the binding to `system:authenticated` refused. It passes the zones of the cluster,
  sorted by name, to the chart as `region.availabilityZones`, from 3 to 9 distinct names, and
  `node_locations` accepts 3 to 9 zones to match.
- Registration keys that start with `rrk_` are redacted from error text.
- `restate_cloud_byok_registration_key` exports `image_pull` (`registry`, `username`, `password`
  (sensitive) and `repository_prefix`), the registry proxy credential that `CreateByokRegistrationKey`
  returns with the key, from the `main` revision of `restatedev/restate-cloud` recorded in
  `pkg/api/cloud/PROVENANCE.md`. It is null when the control plane returns none. `modules/byoc/gcp-byok`
  takes `image_source_mode`, `direct` by default, which pulls every image from Restate's registry proxy
  with a Secret built from `image_pull`, and `mirror`, which keeps the earlier behaviour and still needs
  `image_source_registry`. Direct mode needs a BYOC chart that has `imageSource.mode`.

Resources:

- `restate_cloud_environment` — create, rename, resize, import and destroy Restate Cloud
  environments. Creation and destruction poll to a terminal state; `deletion_protection` guards a
  destroy; a region change replaces the environment because the API has no migration operation.
- `restate_cloud_api_key` — environment-scoped API keys. The bearer secret is available only from the
  create response, so it is stored in state, marked sensitive, and cannot be recovered by an import.
  Every change replaces the key, because the API has no update operation.
- `restate_deployment` — register an HTTP or AWS Lambda service deployment. Registration performs
  service discovery, so timeouts are configurable. Deletion requires `force_destroy`: the API
  implements only forced deletion, which can break in-flight invocations. `force` defaults to
  `false` and is sent explicitly on every registration, because the API's own default is `true` —
  omitting it would let a registration override an existing deployment on the same URI. Removing
  `lambda.assume_role_arn` clears role chaining in place rather than being ignored.
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
- Generated reference documentation, plus an example and import command for every resource and
  data source. The examples all describe one order-processing application, so the snippets across
  the reference fit together, and a complete applyable version of it lives in
  `examples/two-stage-bootstrap/`.

### Changed

- The Cloud Management API snapshot was refreshed to `restatedev/restate-cloud` revision `ea764f52`.
  The operations and fields that existing resources read are unchanged. Upstream now declares the
  `accountId` path parameter, so the `normalizecloudspec` step that injected it was removed and the
  client is generated from the pristine snapshot.

### Correctness notes

Behaviours worth stating explicitly, because they are the ones most likely to surprise:

- **Writes keep planned values; refreshes adopt the API's.** Terraform requires the value after an
  apply to equal what was planned, so every resource verifies that the API applied what it was asked
  for and reports a discrepancy directly, rather than adopting a different value and failing
  Terraform's own consistency check with a message that blames the provider.
- **Request deadlines are layered, not capped.** `request_timeout` is a default that applies only
  where a resource sets no timeout of its own, so a resource-level timeout always wins — deployment
  discovery legitimately takes minutes. Poll loops bound each attempt separately, so one hung request
  costs an attempt rather than a whole provisioning budget.
- **A deployment is replaced only when its endpoint kind changes**, not when a field inside it does.
  Moving a URI is an in-place update; the Admin API supports that, and replacing a deployment breaks
  every in-flight invocation.
- **A value the configuration left unset is never adopted from the API where the API is entitled to
  choose it.** `use_http_11`, deployment headers, Kafka properties the API redacts, and the
  `google_id_token` sub-attributes the contract documents as derivable are all preserved from
  configuration, because adopting a server-chosen value would produce a permanent diff — and, for
  authentication, a deployment replaced on every apply. Genuine out-of-band changes are still
  reported: authentication being removed entirely shows up as drift.
- **A hung poll attempt costs that attempt, not the operation.** An environment create that polls for
  thirty minutes survives a describe that never answers; the tolerance is bounded, and a fetch that
  fails for any other reason — an expired credential — is still fatal immediately rather than being
  reported as a timeout.

### Notes

- Restate Cloud API contract: `v1`, from `restatedev/restate-cloud` at `d9886ea`.
- Restate Admin API contract: `1.7.6`, from <https://docs.restate.dev/schemas/openapi-admin.json>.
- Supported Admin API versions: 2 to 4. The client was generated against version 4 (Restate 1.7.6);
  compatibility with a server whose maximum is 2 or 3 has not been tested and is not advertised.
