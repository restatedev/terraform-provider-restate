# Terraform Provider for Restate Cloud: Development Plan

This repository will contain a Terraform provider for provisioning Restate Cloud environments through the Cloud Management API and configuring them through the per-environment Admin API. Treat this file as both the project plan and the implementation contract. Update the checkboxes and the "Decisions still requiring validation" section as work progresses.

## Goal

Build a production-quality Terraform provider that lets users declaratively provision Restate Cloud environments and environment API keys, then register deployments and manage stable configuration objects inside those environments.

Environment provisioning is a required first-release capability. A user with an existing Restate Cloud account and a `ManagementApiAccess` key must be able to create, read, rename, resize, import, and destroy environments in that account. Do not ship a provider that only configures pre-existing environments.

The provider uses two separate contracts:

- The Restate Cloud Management API provisions environments and environment-scoped API keys within an existing account. The provider vendors its `v1` OpenAPI contract, tests it directly, and absorbs compatible wire-level changes in a handwritten facade.
- The published per-environment Admin API manages deployments, Kafka clusters, subscriptions, and service configuration.

The first release does **not** provision Restate Cloud accounts, account-scoped management API keys, billing configuration, members, groups, or role bindings. Those operations require an interactive user identity. The provider instead uses a pre-created account-scoped `rst:role::ManagementApiAccess` key.

Although the Admin API is shared with self-hosted Restate, design the client against the documented API rather than Cloud-only URL assumptions. A self-hosted compatibility claim is optional and must be tested before it is advertised.

## Documentation survey

Survey date: 2026-08-27.

The documentation crawl started at [the Restate documentation root](https://docs.restate.dev/docs) and used the site's published [documentation index](https://docs.restate.dev/llms.txt). The machine-readable API contract at [openapi-admin.json](https://docs.restate.dev/schemas/openapi-admin.json) reported Admin API version `1.7.6` on the survey date.

The provider therefore consumes two machine-readable contracts: the published Admin API contract and the versioned Cloud Management API contract vendored in this repository. Generated clients, tests, and compatibility facades must keep their authentication and lifecycle semantics separate.

Relevant sources:

- [Getting started with Restate Cloud](https://docs.restate.dev/cloud/getting-started) documents existing environments, environment Admin and ingress URLs, and bearer API keys. It shows that external automation can call the Admin API on port 9070.
- [Connecting services to Restate Cloud](https://docs.restate.dev/cloud/connecting-services) covers HTTP endpoints, AWS Lambda, Cloud Run-style request authentication, private tunnels, signing public keys, and environment/region identifiers.
- [Deployment versioning](https://docs.restate.dev/services/versioning) explains discovery, deployment revisions, compatibility, and forced deletion risk.
- [Service configuration](https://docs.restate.dev/services/configuration) describes public visibility, retry/timeout behavior, and retention settings.
- [Kafka invocation](https://docs.restate.dev/services/invocation/kafka) explains Kafka clusters and subscriptions.
- The [Admin API reference](https://docs.restate.dev/admin-api/deployment/list-deployments) and OpenAPI contract define deployments, services, handlers, Kafka clusters, subscriptions, rules, introspection, invocations, health, and version endpoints.
- The vendored Cloud Management OpenAPI contract defines environment, account lookup, region lookup, and environment API-key operations.

Re-run both surveys before each minor provider release. Pin both OpenAPI snapshots used for generated code and record their advertised versions, provenance, and content checksums.

## API-to-Terraform scope

### Tier 1: resources for the first usable release

1. `restate_cloud_environment` (required for the first release)
   - Backed by `CreateEnvironment`, `DescribeEnvironment`, `ListEnvironments`, `UpdateEnvironment`, `UpdateEnvironmentTier`, and `DestroyEnvironment` on `POST /cloud/{accountId}/{operation}`.
   - Manage `name` and `tier_id` for standard tiers. Although create accepts an operator-configured `profile_id`, the surveyed describe response does not return it; defer custom-profile management until the API exposes enough state for refresh/import without hidden drift.
   - Accept an optional `region`; if omitted, persist the region returned by `DescribeEnvironment` so subsequent plans converge. Treat region changes as replacement unless the Cloud API gains an explicit migration operation.
   - Poll `DescribeEnvironment` after create until `ACTIVE`; fail immediately on `FAILED`, honor resource timeouts, and preserve the environment ID in diagnostics for cleanup. Do not consider the short create response proof that provisioning completed.
   - Update `name` through `UpdateEnvironment`. Update `tier_id` through `UpdateEnvironmentTier` only after validating the account's billing mode and the transition's non-interactive behavior.
   - Do not support Stripe checkout/payment fields in the initial Terraform resource. A response containing `checkoutUrl` means the operation requires interactive payment and must produce an actionable error without claiming convergence. Account-billed transitions may be supported after live tests.
   - Export computed `status`, `signing_public_key`, `ingress_base_url`, `admin_base_url`, `invocation_metrics_url`, `tunnel_base_url`, `tunnel_srv`, `proxy_base_url`, effective region, and effective tier.
   - Treat `DELETED` or a not-found response during refresh as removal from state. Destroy must poll to a terminal deleted/not-found state and be idempotent.
   - Add `deletion_protection` to guard production environments; reject destroy while it is enabled.
   - Import by a documented composite identity containing account ID and environment ID, and verify the imported environment belongs to the configured account.

2. `restate_cloud_api_key` (environment-scoped)
   - Backed by `CreateApiKey`, `DescribeApiKey`, `ListApiKeys`, and `DeleteApiKey` on the Cloud Management API.
   - Manage `environment_id`, `role_id`, and optional `description`. Supported roles are `FullAccess`, `IngressAccess`, `AdminAccess`, `CompleteAwakeableAccess`, and `TunnelAccess` using their current `rst:role::...` wire identifiers.
   - Store the generated key ID as identity. Treat environment or role changes as replacement unless a future API adds update support.
   - Return the secret `api_key` only from create and mark it sensitive. Explain that Terraform state still contains the secret and must be encrypted and access-controlled.
   - Refresh from `DescribeApiKey` without replacing the saved secret with an absent value. A deleted/not-found key is removed from state.
   - Import uses account ID, environment ID, and key ID, but cannot recover `api_key`. Document that imported keys can be observed/deleted but their bearer secret remains unavailable; downstream configuration requiring the secret must supply it separately or replace the key.
   - This resource must never accept or create `rst:role::ManagementApiAccess`; that role is account-scoped and bootstrapped outside Terraform.

3. `restate_deployment`
   - Backed by `POST /deployments`, `GET /deployments/{deployment}`, `PATCH /deployments/{deployment}`, and `DELETE /deployments/{deployment}`.
   - Support exactly one endpoint kind:
     - HTTP: `uri`, `use_http_11`, supported HTTP authentication, additional invocation headers, and metadata.
     - AWS Lambda: `arn`, `assume_role_arn`, compression when supported, additional headers, and metadata.
   - Store the server-generated deployment ID as Terraform resource identity.
   - Treat switching between HTTP and Lambda as replacement.
   - The surveyed update request cannot change HTTP authentication or metadata. Plan replacement for changes to fields the API cannot update; do not silently ignore them.
   - Registration performs service discovery. Set generous, configurable timeouts and return discovery diagnostics clearly.
   - Do not model `dry_run` as persistent resource state. It is an action and cannot result in a valid Terraform create.
   - Treat `breaking` and registration `force` as create-time behavior, not remotely readable configuration. Decide their exact schema only after protocol tests prove that a saved plan remains stable.
   - Updating an endpoint does not rediscover schemas unless `overwrite` is true. Expose this behavior deliberately and warn that overwrite permits breaking schema changes.
   - The API currently only implements forced deletion. Require an explicit `force_destroy = true` before destroy, and explain that deletion can break in-flight invocations.
   - Mark all header values and any authentication material as sensitive. Never emit them in diagnostics or HTTP logs.

4. `restate_kafka_cluster`
   - Backed by create/get/update/delete under `/kafka-clusters`.
   - Manage `name` and the librdkafka-compatible `properties` map. Require either `bootstrap.servers` or `metadata.broker.list` using plan-time validation where possible and server validation otherwise.
   - Treat `name` as immutable because the API addresses updates by name.
   - Mark the properties map sensitive because it can contain passwords and secrets.
   - The API redacts sensitive properties on read. Preserve prior configured secret values when the response contains redaction markers, and prove this with refresh and import tests. If the redaction contract is not deterministic, split known credentials into write-only attributes or defer the resource rather than accepting perpetual diffs.
   - Default to dependency-safe deletion. Do not set the API's `force` query parameter unless the user explicitly enables `force_destroy`, because forced deletion can orphan subscriptions.

5. `restate_subscription`
   - Backed by create/get/delete under `/subscriptions`; there is no update endpoint in the surveyed contract.
   - Manage `source`, `sink`, and string `options`; save the server-generated subscription ID.
   - Validate documented URI forms (`kafka://<cluster>/<topic>` and `service://<service>/<handler>`) without blocking future schemes that a newer server advertises. Prefer warnings or version-gated validation over a permanently closed enum.
   - Changes to source, sink, or options require replacement.
   - Support import by subscription ID.

### Tier 2: add after lifecycle semantics are proven

6. `restate_service_configuration`
   - Backed by `GET /services/{service}` and `PATCH /services/{service}`.
   - Manage only fields accepted by `ModifyServiceRequest`: `public`, `idempotency_retention`, `journal_retention`, `workflow_completion_retention`, `inactivity_timeout`, and `abort_timeout`.
   - The service is discovered from a deployment; this resource neither creates nor deletes the service itself. Document the dependency and allow a natural reference to `restate_deployment` outputs.
   - Normalize duration values using a canonical representation so equivalent friendly and ISO-8601 durations do not drift.
   - Before implementation, verify what JSON `null` means for every field, how to restore discovery/default values on resource destroy, and what happens when deployment rediscovery overwrites settings. If the API cannot provide safe delete/reset behavior, expose service configuration as an explicitly partial-management resource or defer it.
   - Workflow completion retention is valid only for workflow services; diagnose this using the service type returned by the API.

### Data sources

Implement focused lookups before broad list data sources:

- `restate_cloud_environment` lookup by account and environment ID, exposing the same non-secret computed connection metadata as the resource.
- `restate_cloud_regions` backed by `ListRegions` for the configured account.
- `restate_cloud_account` backed by `DescribeAccount` and optionally `GetAccountLimits`, exposing billing mode, subscription status, and limits without managing them.
- `restate_cloud_account_usage` backed by `GetAccountUsage` only after its time-range and potentially large/sensitive response semantics are bounded.
- `restate_deployment` lookup by deployment ID, or use the resource as its own import/read path if provider naming conventions make a duplicate confusing.
- `restate_service` lookup by service name, including type, revision, handlers, and effective configuration.
- `restate_service_handler` lookup by service and handler name.
- `restate_kafka_cluster` lookup by name, subject to the same secret-redaction rules as the resource.
- `restate_subscription` lookup by ID.
- `restate_server_info` backed by `GET /version`, exposing the server version, minimum and maximum Admin API versions, and advertised ingress endpoint.
- Optionally add list data sources only after users demonstrate a Terraform expression use case. Avoid adding an untyped SQL-query data source merely because `/query` exists.

### Explicitly deferred or excluded

- **Cloud accounts, account management keys, billing, members, groups, and role bindings:** their mutation APIs require a human user principal. Do not put short-lived Cloud user JWTs in Terraform or automate interactive login. Reconsider only if Restate publishes machine-principal permissions with safe lifecycle semantics.
- **Interactive Stripe checkout:** Terraform cannot complete browser payment flows. Detect and reject operations requiring `checkoutUrl` rather than leaving partially managed state.
- **Custom environment profiles:** create accepts `profileId`, but describe does not report it. Do not manage a write-only placement/size choice that cannot be refreshed or imported safely.
- **Cloud Management API operations absent from its machine-principal policy:** do not assume that appearing in the generated OpenAPI document makes an operation callable by `ManagementApiAccess`.
- **Flow-control rules:** the batch upsert and pattern-delete endpoints are persistent-looking, but the published Admin API has no direct get/list endpoint. Investigate an officially supported read path and stable identity before designing a Terraform resource.
- **Invocation cancel, kill, pause, resume, purge, journal purge, and restart-as-new:** these are imperative runtime operations, not convergent infrastructure.
- **Virtual Object state mutation:** `POST /services/{service}/state` mutates application data and must not be represented as infrastructure state.
- **Health and cluster state:** useful for provider configuration checks or diagnostics, not managed resources.
- **Service OpenAPI documents and SQL introspection:** possible data sources only when there is a concrete declarative use case and bounded/sensitive output behavior.
- **Tunnel processes and Kubernetes CRDs:** users should manage these through their container/Kubernetes provider and the Restate Operator. This provider may expose the environment metadata they need only when a supported API makes it available.

## Provider configuration and behavior

Use the Terraform Plugin Framework in Go. Do not shell out to the Restate CLI.

Proposed configuration:

```hcl
provider "restate" {
  cloud_api_url      = "https://api.us.restate.cloud/cloud"
  account_id         = var.restate_cloud_account_id
  management_api_key = var.restate_cloud_management_api_key

  # Optional when this provider instance also manages an existing environment.
  admin_url           = var.restate_admin_url
  environment_api_key = var.restate_environment_api_key
}
```

- Cloud Management configuration consists of `cloud_api_url`, `account_id`, and `management_api_key`, defaulting from `RESTATE_CLOUD_API_URL`, `RESTATE_CLOUD_ACCOUNT_ID`, and `RESTATE_CLOUD_MANAGEMENT_API_KEY`. The key must be account-scoped with role `rst:role::ManagementApiAccess` and must match `account_id`.
- Default the Cloud URL to `https://api.us.restate.cloud/cloud`, while allowing an explicit override for other supported realms and testing. Do not infer the management endpoint from an environment's region.
- `management_api_key` is sensitive, is sent as a bearer token only to the Cloud Management API, and is never persisted in resource state. It is distinct from environment-scoped API keys and must never be sent to an environment Admin API.
- `admin_url` defaults from `RESTATE_ADMIN_URL` and must be a complete Admin API base URL. Do not construct it from an environment ID or assume current `*.env.<region>.restate.cloud:9070` hostname formatting.
- `environment_api_key` defaults from `RESTATE_AUTH_TOKEN`, is sensitive, and is sent only to the configured environment Admin API. Never persist provider credentials as resource state.
- Permit Cloud-only, Admin-only, or combined provider configurations. Reject a partially specified credential set during provider configuration; if a complete client is absent, the resource/data source that requires it must return a specific configuration diagnostic.
- Support provider-level request timeout and a stable user agent containing provider and Terraform versions.
- Normalize API URLs by removing only trailing slashes; preserve deliberate path prefixes such as `/cloud`.
- Require HTTPS by default for non-loopback hosts. If an insecure override is added for local/self-hosted testing, name and document it explicitly.
- Configure immutable, concurrency-safe Cloud and Admin clients per provider instance as their credential sets permit. Terraform aliases must permit multiple accounts/environments in one configuration.
- Validate Cloud credentials with a read-only account-scoped operation such as `DescribeAccount`; the Cloud API currently has no version endpoint. Treat the pinned `v1` snapshot as the compatibility boundary.
- When Admin configuration is present, call `GET /version` unless configuration is unknown. Verify that the server's advertised Admin API range overlaps the client's supported range and produce an actionable diagnostic when it does not.
- Use `GET /health` only as a bounded connectivity diagnostic. Do not make transient health state create unrelated plan drift.
- Implement context cancellation, bounded retries with jitter for transport errors, `429`, and retryable `5xx` responses. Do not retry validation/authentication errors or non-idempotent creates unless idempotency is established.
- Parse the documented error body and preserve status code and request/correlation ID, while redacting authorization, headers, Kafka secrets, and response bodies that may contain them.
- Bound response sizes and use Terraform resource timeouts for discovery-heavy deployment calls.

### Bootstrap and apply-stage boundary

Terraform provider configuration must be known before resource operations. An `admin_url` and environment key created by `restate_cloud_environment` and `restate_cloud_api_key` cannot safely configure an aliased provider instance during the same apply.

- Support Cloud provisioning and Admin configuration as two explicit stages for the MVP, normally separate Terraform states/workspaces or two applies with the connection values passed through a secret store.
- Do not claim that `provider "restate" { admin_url = restate_cloud_environment.x.admin_base_url }` works during initial creation; Terraform cannot configure that provider until the resource exists.
- Provide a guide showing stage 1 creating the environment and an `AdminAccess`/`FullAccess` key, securely exporting the Admin URL and secret, and stage 2 configuring the Admin API provider to manage deployments and other environment objects.
- Do not duplicate environment credentials on every Admin resource merely to bypass provider initialization. Reconsider a dynamic per-resource connection model only if users require single-apply bootstrapping and the security/state tradeoff is accepted explicitly.

## Repository and implementation shape

Initialize the project with the conventional provider layout:

```text
.
├── AGENTS.md
├── README.md
├── go.mod
├── main.go
├── docs/
│   ├── data-sources/
│   ├── guides/
│   └── resources/
├── examples/
│   ├── data-sources/
│   ├── provider/
│   └── resources/
├── pkg/
│   └── api/
│       ├── admin/
│       │   ├── openapi.json
│       │   ├── generate.go
│       │   └── internal/generated/
│       ├── cloud/
│       │   ├── openapi.yaml
│       │   ├── generate.go
│       │   └── internal/generated/
│       ├── client.go
│       ├── operations.go
│       └── errors.go
├── internal/
│   ├── provider/
│   └── testutil/
├── tools/
└── .github/workflows/
```

- Use a pinned Go toolchain and commit `go.sum`.
- Make `pkg/api` the only package that knows either wire contract. The Terraform provider must consume its Cloud/Admin facades instead of issuing HTTP requests itself.
- Pin the published Admin contract at `pkg/api/admin/openapi.json` from `https://docs.restate.dev/schemas/openapi-admin.json`.
- Pin the Cloud Management contract at `pkg/api/cloud/openapi.yaml` from an authoritative reviewed upstream revision.
- Record license/provenance, API version, upstream revision, and checksum beside each snapshot. `go generate ./pkg/api/...` must deterministically regenerate each client into its own `internal/generated` directory; CI must fail on a generated diff.
- Pin one OpenAPI generator and its configuration as a Go tool dependency. Generate typed request/response models and the HTTP client from the spec; never hand-maintain endpoint structs that the spec can generate.
- Keep generated names, nullability workarounds, and `oneOf` mechanics inside the relevant internal generated package. Handwritten `pkg/api/cloud` and `pkg/api/admin` facades are the supported Go APIs and must expose stable domain types so generated-code churn does not propagate into Terraform schemas or external consumers.
- Share authentication-neutral transport/error/generic helpers at `pkg/api`, but keep Cloud and Admin authentication options distinct so a management key cannot be sent to an environment and vice versa.
- Keep `pkg/api` independent of Terraform packages. It may return typed domain values and typed API errors, but it must not return `diag.Diagnostics`, framework values, or resource state models.
- Keep transport, API models, Terraform schema/state conversion, and resource lifecycle logic separate.
- Centralize duration normalization, URI validation, redaction-aware map merging, not-found detection, and diagnostics.
- Use framework validators and plan modifiers. Do not use diff suppression to hide unexplained drift.
- Add `terraform-plugin-docs` generation from schema descriptions and runnable examples. Generated docs must not replace the hand-written security and lifecycle guide. `docs/` is generated in its entirety, so handwritten pages live in `templates/guides/` and are copied through; anything placed directly in `docs/` is deleted by the next generation.
- Add standard licensing, contribution, security reporting, changelog/release automation, and Terraform Registry metadata before publication.

### Generic API operations

Use Go generics in the handwritten `pkg/api` layer to implement the common mechanics once while preserving endpoint-specific behavior. The intended shape is a set of small capabilities, not one universal reflection-based CRUD engine:

```go
type Getter[ID comparable, Remote any] interface {
	Get(context.Context, ID) (Remote, error)
}

type Creator[Input any, Remote any] interface {
	Create(context.Context, Input) (Remote, error)
}

type Updater[ID comparable, Input any, Remote any] interface {
	Update(context.Context, ID, Input) (Remote, error)
}

type Deleter[ID comparable, Options any] interface {
	Delete(context.Context, ID, Options) error
}
```

- Put reusable typed request execution, response decoding, status/error conversion, not-found handling, and optional list pagination in generic helpers in `pkg/api/operations.go`.
- Implement endpoint adapters for Cloud environments, environment API keys, deployments, Kafka clusters, subscriptions, services, and server information using the appropriate generated client. Compile-time interface assertions must prove which capabilities each endpoint supports; for example, subscriptions and Cloud API keys deliberately have no `Updater`.
- Parameterize identifiers, inputs, and remote models so resources cannot accidentally pass a subscription ID to a deployment operation or use a create request as an update request.
- Let each endpoint adapter decide operation-specific query parameters, idempotency/retry safety, redaction behavior, and response-union decoding. These differences must not be hidden behind a generic default.
- Keep Terraform reconciliation in `internal/provider`. Generic API capabilities may remove request boilerplate, but resource-specific plan modifiers, replacement rules, state preservation, import, destroy protection, and diagnostics remain explicit.
- Avoid reflection, `map[string]any`, type switches over every resource, or a generic repository that assumes every object has symmetrical CRUD. If an abstraction makes an endpoint's destructive or partial lifecycle less obvious, prefer a small explicit adapter.
- Unit-test generic helpers once with synthetic types, then contract-test every endpoint adapter against `httptest.Server` fixtures generated or derived from the pinned OpenAPI contract.

## State and lifecycle rules

- Every resource must implement create, read/refresh, update when the API supports it, delete, and import before it is considered complete.
- A `404` during refresh removes the resource from state. A `404` during delete is success.
- Import must populate enough state for the next plan to be empty, except write-only/redacted secrets that the API cannot recover. Document any required post-import secret configuration.
- Distinguish absent, null, empty map, and server default values. Verify behavior with captured API fixtures rather than guessing from Go zero values.
- Preserve unknown values during planning and never replace them with empty values.
- Mark secret-bearing attributes sensitive at every nesting level. State is still sensitive even when CLI output is redacted; documentation must tell users to secure remote state.
- Avoid accidental ownership of computed deployment discovery fields such as services, handlers, SDK version, protocol range, and creation time. Export them as computed values only.
- Avoid accidental ownership of computed Cloud environment fields such as provisioning status, signing key, URLs, and account-derived defaults. Export them as computed values and use them for drift detection only where the API provides a corresponding mutation.
- Reference relationships should work naturally: a subscription can reference a Kafka cluster name and service/handler data; service configuration can depend on a deployment.
- All destructive escape hatches must be opt-in, clearly named, tested, and documented.

## Testing strategy

### Unit and client contract tests

- Run both OpenAPI generators in CI and require a clean tree, then compile each generated package independently before testing its facade.
- Use `httptest.Server` fixtures for every method, including bearer authentication, URL escaping, query parameters, request bodies, success responses, structured errors, malformed bodies, pagination if later introduced, and cancellation.
- Derive fixtures from the pinned OpenAPI version, but add hand-written edge cases for `oneOf`, nullability, defaults, and redacted secrets.
- Add facade contract tests for configurable server URLs, account management-key bearer authentication, and every success/error response the provider handles, including `403` and `404`.
- Test retry classification and prove that secrets never occur in diagnostic strings or debug logs.
- Test Admin `/version` compatibility at the lower boundary, upper boundary, no-overlap case, and malformed response. Test Cloud snapshot/source-commit compatibility separately because it has no version endpoint.

### Terraform protocol tests

- Use `terraform-plugin-testing` for plan/state behavior.
- For each resource, test create/read, update or replacement, drift refresh, import, out-of-band deletion, and destroy protection.
- Include unknown-value plans, aliased providers, special characters in IDs/names, empty versus null collections, and sensitive-value assertions.
- Add regression tests for Kafka redaction so refresh does not produce a perpetual diff or erase credentials from state.
- Add regression tests for deployment discovery timeouts and replacement between endpoint kinds.
- Add regression tests for Cloud environment asynchronous creation/failure/deletion, deletion protection, immutable region/profile replacement, interactive checkout rejection, composite imports, and preservation of create-only API-key secrets.

### Acceptance tests

- Gate Admin live tests behind `TF_ACC=1`, `RESTATE_ADMIN_URL`, and `RESTATE_AUTH_TOKEN`.
- Gate Cloud Management live tests behind `TF_ACC=1`, `RESTATE_CLOUD_API_URL`, `RESTATE_CLOUD_ACCOUNT_ID`, and `RESTATE_CLOUD_MANAGEMENT_API_KEY`.
- Use unique, prefixed names and metadata for every run. Never target a shared object not created by the test.
- Separate Cloud acceptance tests from optional self-hosted tests and report their results independently.
- Run read-only Cloud account/region and Admin server-info tests before destructive tests. Run environment provisioning and destructive deployment tests only in explicitly designated test accounts/environments.
- Acceptance cleanup must be idempotent and bounded. Log identifiers, never credentials or user payloads.
- Test the minimum and maximum supported Restate versions before expanding the compatibility statement.

## Delivery plan

### Phase 0 - contract and UX proof

- [x] Initialize Git, Go module, license, contribution/security files, and basic CI.
- [x] Save and checksum both surveyed OpenAPI contracts under `pkg/api`; record their provenance, pin a generator, and add deterministic `go generate` and generated-diff checks.
- [x] Document the provider's Cloud Management API compatibility and upgrade policy (`pkg/api/cloud/PROVENANCE.md`).
- [x] Define the public Cloud/Admin domain types and shared generic capability interfaces; keep generated types internal.
- [x] Record the supported Admin API range and decide the provider/version compatibility policy (`pkg/api/admin/version.go`).
- [x] Build read-only Cloud probes for management-key authentication, account matching, region listing, error bodies, and configurable base URLs. Exercised against `httptest` fixtures; live probing is pending credentialled access.
- [ ] Build disposable **live** lifecycle probes for asynchronous environment create/update/tier/destroy, environment API-key secret/read/delete semantics, deployment CRUD, Kafka secret redaction, subscription replacement, and service-config null/reset behavior. Contract-level behaviour is modelled and tested against fixtures; the open items are recorded under "Decisions still requiring validation".
- [x] Finalize Tier 1 resource schemas from the pinned contracts, and capture the decisions in this file. Schemas that depend on unresolved live behaviour are held back rather than guessed at.

Exit criteria: all Tier 1 fields have known lifecycle/authentication semantics, the provider relies only on the pinned Cloud and Admin contracts, and unresolved behavior is explicitly deferred.

### Phase 1 - generated clients and generic API foundation

- [x] Generate typed Cloud and Admin clients/models from their pinned specifications.
- [x] Implement Terraform-independent Cloud/Admin facades, typed errors, generic request helpers, and endpoint capability adapters.
- [x] Add compile-time capability assertions and generated/facade contract tests.
- [x] Implement dual provider configuration, strict credential separation, environment variables, URL normalization, timeout/retry policy, redaction, Cloud account validation, and Admin version negotiation.
- [x] Add client unit tests and Terraform provider configuration tests.
- [x] Implement `restate_server_info`.

Exit criteria: Cloud-only, Admin-only, and combined configurations have passing read-only acceptance tests, and wrong account/key pairs, invalid credentials, or Admin version mismatches yield safe diagnostics.

### Phase 2 - required Cloud environment and key MVP

- [x] Implement `restate_cloud_environment` with create polling, computed connection metadata, name/tier updates, replacement rules, import, deletion protection, and terminal deletion polling.
- [x] Implement `restate_cloud_api_key` with role validation, create-only sensitive secret preservation, import limitations, out-of-band deletion, and destroy.
- [x] Implement Cloud environment, regions, and account lookup data sources.
- [x] Add a two-stage bootstrap guide and examples that transfer credentials only through an access-controlled secret store, plus guides on state/secret handling and every destructive operation.
- [ ] Test account-billed tier transitions against the **live** API. Interactive checkout is rejected deterministically and is covered by protocol tests; which transitions are non-interactive for each billing mode remains unresolved (decision 10).

Exit criteria: environment and key resources converge across apply/refresh/import/destroy in a designated account, create failure leaves an actionable identifier, secrets never appear in logs, and the documented bootstrap does not imply same-apply provider reconfiguration.

Release gate: do not publish the provider's first usable release until `restate_cloud_environment` passes its complete unit, protocol, import, and live acceptance lifecycle. Environment support may not be deferred in favor of Admin-only resources.

### Phase 3 - deployments MVP

- [x] Implement `restate_deployment` for HTTP endpoints.
- [x] Add Lambda endpoint support and the authentication field. Compression is **read-only**: the pinned contract reports it but accepts it on neither register nor update, so it cannot be configured.
- [x] Implement refresh, update, import, out-of-band deletion, timeouts, and guarded forced destroy.
- [x] Export computed discovery metadata without taking ownership of it.
- [x] Generate documentation and HTTP/Lambda examples.

Exit criteria: repeated plan after apply and import is empty; HTTP and Lambda acceptance cases pass; no secret appears in test output.

### Phase 4 - Kafka and subscriptions

- [x] Implement `restate_kafka_cluster` with redaction-aware state behaviour, proven against fixtures: a redacted or sensitively-named property keeps its configured value, non-secret drift is still reported, and the merge is idempotent so refresh cannot oscillate. The redaction marker itself is a heuristic pending live confirmation (decision 5).
- [x] Implement `restate_subscription` with replacement semantics.
- [x] Implement focused lookup data sources for both.
- [x] Test dependency ordering and protected versus forced Kafka deletion.

Exit criteria: a live Kafka cluster/subscription configuration converges across refresh and import, or the Kafka resource is explicitly held back if the API cannot safely reconcile secrets.

### Phase 5 - services

- [x] Implement service and handler lookup data sources.
- [x] Implement `restate_service_configuration` as an **explicitly partial-management** resource, which is the option this plan permits when reset behaviour cannot be established. It sends only the attributes that are set and never a JSON null; destroy and attribute removal stop management and warn that the applied value was left in place. Reset semantics remain unresolved (decision 6) and the resource does not pretend otherwise.
- [x] Add duration canonicalization and service-type validation. Durations use a semantic-equality attribute type so `1 day`, `24h` and `P1D` compare equal; a calendar duration such as `P1M` has no fixed length and falls back to exact comparison rather than being declared equal to something it is not.
- [x] Document ownership interaction between SDK discovery and Terraform overrides.

Exit criteria: an apply/refresh cycle remains stable after deployment rediscovery and resource removal has defined, tested behavior.

### Phase 6 - release readiness

- [x] Add formatting, vet, race-enabled unit and protocol, module-tidiness, generated-file, documentation-diff, example-formatting, whitespace, and vulnerability CI jobs, plus a manually triggered acceptance workflow.
- [x] Add examples and import instructions for every resource and data source, plus state-security and destructive-operation guidance. Upgrade notes await a first release to upgrade from.
- [x] Document both API sources, pinned revisions, known contract gaps, and the compatibility policy, in each contract's `PROVENANCE.md`.
- [ ] Test with a clean checkout on all supported Go/Terraform/Restate versions. A CI matrix over Terraform and Restate versions is still needed, and the supported Restate range must not be advertised beyond what has been tested (decision 1).
- [x] Configure reproducible signed releases and Terraform Registry publishing: trimmed static builds, a checksum manifest, a detached GPG signature, and a registry protocol manifest. Releases are drafted for review rather than published straight to the registry.
- [ ] Publish a prerelease, collect schema/lifecycle feedback, then declare `v1.0.0` compatibility guarantees. Blocked on the live acceptance run, which is the documented release gate for `restate_cloud_environment`.

Exit criteria: CI and designated Cloud acceptance tests pass, generated docs match schemas, release artifacts are verifiable, and every advertised resource has an import path and lifecycle coverage.

### Phase 7 - post-MVP candidates

- [ ] Re-crawl the Cloud and Admin documentation and refresh both contract snapshots.
- [ ] Refresh the vendored Cloud contract from authoritative releases while preserving Terraform schema and state compatibility.
- [ ] Reconsider Cloud accounts, management keys, billing, and RBAC only if supported machine-principal operations exist.
- [ ] Reconsider flow-control rules once a supported read/list contract exists.
- [ ] Evaluate list/OpenAPI/SQL data sources from demonstrated user needs.
- [ ] Evaluate and test an explicit self-hosted compatibility mode.

## Decisions still requiring validation

Do not silently choose answers to these during implementation; resolve them with the pinned contract plus live API tests and update this section:

1. **Partly resolved.** The provider declares support for Admin API versions 2 to 4 and requires the server's advertised range to overlap it; overlap below version 4 -- the version the pinned contract documents -- is a warning rather than an error, and a disjoint range fails provider configuration. The bounds come from Restate's own `MIN_ADMIN_API_VERSION`/`MAX_ADMIN_API_VERSION` (V2..V4 as of the surveyed 1.7.6 contract), and version 4 was introduced in Restate 1.7.0. The contract exposes no per-request version selector, so this is a compatibility gate, not a negotiation that downgrades request shapes.

    **Still open:** which Restate versions the release actually claims. The provider's request and response shapes come from the 1.7.6 contract, so compatibility with a server whose maximum is 2 or 3 is asserted by range arithmetic, not by testing. Do not advertise a minimum supported Restate version below 1.7.0 until it has been tested against a real server.
2. **Resolved for the pinned contract.** `force`, `breaking` and `overwrite` are plain optional booleans defaulting to false in the Terraform schema. They are configuration-only: the API neither accepts them on the operations they do not apply to nor reports them, so they are never read back and cannot drift. A saved plan is stable because the value comes from configuration rather than from the API.

    One of the three does **not** default to false on the wire: the contract declares `force: default true`, and forcing overrides any existing deployment on the same URI, which "can lead inflight invocations to an unrecoverable error state" and implies `breaking = true`. Omitting the field therefore leaves the destructive server default in charge, so the facade sends `force` explicitly on every registration -- including `false` -- while still omitting `breaking`, `use_http_11` and the rest, whose server default is already the safe one. That asymmetry is deliberate and tested; a refreshed snapshot that changes any of these defaults must be re-checked against `boolPointer`/`truePointer` at the registration call sites.

    The remaining risk was silently ignoring a change, and that is handled explicitly rather than suppressed: changing `force` or `breaking` on an existing deployment emits a warning saying the value applies only when the resource is next created. `RequiresReplace` was rejected as the alternative — destroying a working deployment because someone flipped a create-time flag is worse than a warning. Protocol tests cover the empty plan and the warning.

    **Still open:** none for this contract. Revisit only if a snapshot starts reporting these fields, in which case they become readable state and should be reconciled.
3. **Handled, not resolved.** Clearing a Lambda deployment's `assume_role_arn` in place. The contract types the update field as `["string","null"]`, and omission and explicit `null` are different requests, but the generated `*string` with `omitempty` can only express omission. `marshalLambdaDeploymentUpdate` is a narrow handwritten wire override that sends explicit `null` when the configuration removes the attribute, so removing it stops role chaining instead of being silently ignored.

    **Still open:** whether the server actually honours explicit `null` as "clear" rather than treating it the same as omission. If it treats them the same, removing `assume_role_arn` would report success while leaving role chaining in place -- so the write verification in `applyDeployment` reports the mismatch rather than converging silently. Confirm against a live server before advertising in-place clearing; if `null` is ignored, the attribute needs `RequiresReplace` instead.

4. Whether a newer API makes deployment metadata or HTTP authentication updateable. In the pinned contract neither is in the update request, so both carry `RequiresReplace`, and `UpdateHTTPDeployment`/`UpdateLambdaDeployment` deliberately have no field for them — there is nothing an update could silently drop. Re-check on the next snapshot; making either updateable would be a schema-compatible improvement.

    Because authentication carries `RequiresReplace`, its refresh behaviour is load-bearing in a way the other attributes' is not: adopting a value the server chose would replace a working deployment on every apply. Both `google_id_token` sub-attributes are documented as derivable from the deployment when left unset, so `mergeHTTPAuth` takes the *presence* of authentication from the API — authentication removed out of band is real drift — while keeping a sub-attribute the configuration left null as null. The write-side check verifies only sub-attributes the configuration actually set, for the same reason `reconcileString` leaves a null planned value alone.

    **Still open:** whether the server ever reports a derived `audience` or `impersonate_service_account`. Until that is known, preserving configuration is the conservative direction; an out-of-band edit to a sub-attribute the configuration left unset is not detected, which is the accepted cost.

    Related and also unresolved: `use_http_11` cannot be read back. The API reports the negotiated `http_version`, and ALPN can produce HTTP/2 for a deployment registered with `use_http_11`, so the two are not the same fact. The provider keeps the configured value and exports `http_version` separately. Confirm against a live server whether any field reports the requested setting.
5. **Handled, not resolved.** The contract says sensitive Kafka properties are redacted on read but does not specify the marker. `MergeRedactedProperties` therefore does not rely on recognising one: it keeps the configured value for any property whose value looks like a placeholder **or** whose name marks its value as secret, and takes the API's value otherwise. That converges whichever marker the server uses, still reports non-secret drift, and is idempotent so refresh cannot oscillate. Import has no configured value to fall back on, so state holds the API's placeholders and the provider warns.

    **Still open:** the actual marker. Capture it from a live cluster and add it to `RedactionMarkers`; the substring-based name matching is the safety net until then.
6. **Handled, not resolved.** The contract does not define what a JSON `null` means for a service-configuration field, and there is no reset operation. `restate_service_configuration` therefore never sends a null, sends only the attributes that are set, and is documented as an explicitly partial-management resource: destroy and attribute removal stop management, leave the applied value in place, and warn. An entirely empty resource is rejected at validation rather than applying as a silent no-op.

    **Still open:** whether a null restores the SDK-discovered value or a server default. If it restores the discovered value, destroy could become a real reset, which would be a behaviour change worth a major release.
7. **Partly resolved.** Retry safety is a per-operation property, not a transport setting, and the zero value is the safest: retry only responses that prove the request was not acted on (429, and 503 with `Retry-After`). Creates use that level; idempotent writes additionally retry retryable 5xx; only reads retry transport errors, where the provider cannot tell whether the server saw the request. `Retry-After` is honoured but clamped, so a broken server cannot park an apply.

    Relatedly, request deadlines are layered rather than capped. `request_timeout` is a **default** applied only when the caller's context has no deadline, so a resource timeout always wins -- an `http.Client.Timeout` would silently override the ten minutes deployment discovery needs. Poll loops bound each attempt separately, so one hung describe costs an attempt rather than the whole provisioning budget: an attempt that exhausts `AttemptTimeout` is a slow server rather than a failed operation, so the loop presses on. That tolerance is bounded (`maxConsecutiveAttemptTimeouts`) so an endpoint that never answers still fails even when the caller set no deadline, and it applies only to the attempt deadline -- a fetch that fails for any other reason is still fatal on the first occurrence, so an expired credential is never reported as a timeout.

    **Still open:** whether the Cloud API actually sends `Retry-After`, and whether any create has an idempotency guarantee. Until it does, no create is retried through an ambiguous 5xx — which is why a failed create records the object's ID in state so Terraform can destroy it rather than leaving an orphan.
8. **Partly resolved.** The pinned `v1` snapshot **is** the compatibility boundary; the API has no version endpoint to negotiate against. The policy is recorded in `pkg/api/cloud/PROVENANCE.md`: send only what the snapshot defines, ignore unknown response fields, fail with an actionable diagnostic when a required field is missing, and treat a refreshed snapshot that changes a field the provider reads as a breaking provider change.

    **Still open:** the canonical base URL for realms other than `api.us.restate.cloud`, and whether upstream will commit to a compatibility guarantee.
9. **Partly resolved.** The provider treats create and destroy as asynchronous and polls to a terminal state; `FAILED` fails immediately rather than at the timeout, and both a 404 and a `DELETED` status count as absence. A create that fails after the API assigned an ID records that ID in state so it can be destroyed.

    **Still open:** the real transition timings, and whether a `FAILED` environment is ever cleaned up server-side. The default create and delete timeouts (30 minutes) are guesses and should be revised from live data.
10. Which tier transitions are non-interactive for each billing mode. A `checkoutUrl` in a create or tier-change response is refused as an error rather than reported as success, the single-use payment link is never echoed into the diagnostic, and a partially applied update is re-read so state reflects what was applied rather than what was planned. `restate_cloud_account.billing_mode` is exposed so a configuration can check before planning a change. The mapping from billing mode to non-interactive transitions still needs live confirmation.
11. **Resolved.** The contract has no key update operation at all, so `description` forces replacement along with everything else, and `restate_cloud_api_key` implements no `Updater` — its `Update` method is unreachable and says so. Import populates every attribute except `api_key`, which is null with a warning explaining that the secret cannot be recovered and that replacing the key is the only way to obtain a new one. The first plan after import is empty.
12. Whether the Cloud API will expose an environment's `profileId` on describe/list. Until it does, `CUSTOM` is readable but not requestable, and `profileId` is never sent — a write-only placement choice that cannot be refreshed or imported would produce permanent hidden drift.
13. Whether either contract gains pagination, ETags, request IDs, idempotency keys, or new authentication schemes in a newer snapshot. Note that the provider already reads a correlation ID from several response headers and preserves it in every error, so a server that starts sending one needs no change.
14. ~~Which pinned OpenAPI generator best preserves both contracts' unions and nullability without requiring edits to generated files.~~ **Resolved:** `oapi-codegen` v2.8.0, pinned as a Go tool dependency. `ogen` v1.24.0 was evaluated first and rejected: it cannot generate the Admin API's untagged deployment `oneOf` ("discriminator inference is not implemented") without skipping the deployment registration operation entirely, and it rejects the Cloud contract outright. `oapi-codegen` renders each union as a raw JSON envelope with typed accessors, which the handwritten facades resolve explicitly. Two generator settings are load-bearing and documented in each `oapi-codegen.yaml`: `skip-prune`, because both contracts cross-reference schemas the retained operations need, and `response-type-suffix`, because the Cloud contract's own `<Operation>Response` schemas would otherwise collide with the generated response wrappers.

    A separate finding: the vendored Cloud contract is **invalid** as published. It templates `{accountId}` into account-scoped paths without declaring the path parameter, which every conforming generator either rejects or silently mis-handles. Upstream has since declared the parameter, and the normalizer that injected it was removed when the snapshot was refreshed on 2026-11-12.
15. **Handled, not resolved.** `restate_cloud_byok_registration_key` mints a one-use BYOK registration key with `CreateByokRegistrationKey`. The key is shown once and expires after four hours, and the API has no operation that reads a key back or revokes an unused one, so the resource is create-only: Read keeps the state, every argument except `deregister_region_on_destroy` replaces the key, and there is no import. The key reserves a region id, but the region exists only after the BYOC chart has registered, so `DeleteByokRegion` answers 404 before that and the resource treats it as success. A destroy, and a replacement, call `DeleteByokRegion` only when `deregister_region_on_destroy` is true, and the default is false in both the resource and the `gcp-byok` module, because a registered region is a customer's running infrastructure, and Restate itself refuses while the region hosts environments. The display name is sent only when the key is created and the API cannot change it afterwards, so it replaces the key as well. The `gcp-byok` module installs the BYOC chart with `wait = false`, because the chart's Connector pod starts only after the registration, and a failed wait can lead a later apply to install the chart again, which replaces the break-glass token with one Restate never saw. The Cloud snapshot is pinned at a `main` revision of `restatedev/restate-cloud` that includes the merge of #2699, because only revisions from that merge on return `regionId` from `CreateByokRegistrationKey`.

    **Still open:** nothing here has run against a live API.

16. **Handled, not resolved.** `restate_cloud_byoc_region_access` is a separate resource keyed by the region ID, not arguments of `restate_cloud_byoc_region`, because the region replaces itself on almost every change and has no update operation, while access is set in place on an `ACTIVE` region and can be changed, removed or imported on its own. `SetRegionAccess` replaces the whole setting, so the resource sends every argument each time and its destroy sends none. A null `entry_source_cidrs` (every address) and an empty one (no address) are different settings and stay different in the schema, the request and the state. The vendored snapshot is the head of restatedev/restate-cloud#2939, which is not merged.

    **Still open:** the snapshot must be replaced with the merge commit's before release; the 409 for "not built yet" is told apart from the other 409s by describing the region, because the contract does not give them codes; and nothing here has run against a live API.

## Known gaps at the time of writing

These are things a reader should not have to discover by reading the code.

- **No live validation has been performed.** Every behaviour in this provider is derived from the two
  pinned contracts and tested against fixtures and a stateful fake. The acceptance tests exist and are
  gated, but they have not been run: no credentials were available. The release gate in Phase 2 --
  `restate_cloud_environment` passing a live lifecycle -- is therefore **not met**, and the provider
  must not be published until it is.
- **The default timeouts are estimates.** 30 minutes for environment create and delete, 10 minutes for
  deployment registration, 60 seconds per request where a resource sets no timeout of its own, and 30
  seconds per poll attempt. They are deliberately generous rather than measured, and should be revised
  from live data.
- **The Kafka redaction marker is a heuristic.** The provider does not depend on recognising it -- see
  decision 5 -- but the marker list should be corrected from a live cluster.
- **Self-hosted Restate is untested and unadvertised.** The Admin API client is built against the
  published contract rather than against Cloud-only URL assumptions, so it should work, but "should"
  is not a compatibility claim. Do not advertise one until Phase 7's explicit self-hosted mode is
  tested.
- **No CI matrix over Terraform or Restate versions.** CI runs one Go and one Terraform version, and
  the Restate version is whatever a fixture says. Phase 6 is not complete without a matrix.
- **The provider test suite is slow, and nothing has been done about it beyond a generous timeout.**
  `internal/provider` drives a real Terraform CLI once per test across ~145 tests, which takes about
  six minutes normally and nineteen under `-race` on a developer machine; CI allows sixty. No test
  calls `t.Parallel()`, because `clearProviderEnvironment` uses `t.Setenv`, which forbids it. Moving
  that clearing into a `TestMain` would unlock parallelism for all but the two tests that set
  provider environment variables deliberately, and is the obvious next move if the wall-clock starts
  to hurt. It is deliberately not done here: mass-parallelizing an acceptance suite is a change whose
  failure mode is flakiness, which is worse than slowness.
- **One test cannot run under the race detector.** `TestAliasedProvidersAddressDifferentAccounts`
  skips itself under `-race`, because terraform-plugin-testing serves aliased provider configurations
  from one in-process server whose configure path races inside the framework rather than in provider
  code. Real Terraform runs a plugin instance per configuration, so the race cannot happen in
  practice. CI runs that test in a separate pass without `-race` so it is not silently skipped
  forever; any further race-skipped test must be added to that pass.
- **`restate_service_configuration` cannot revert.** This is a documented consequence of the contract,
  not an oversight, but it is the most surprising behaviour in the provider and is called out in the
  destructive-operations guide as well as on the resource itself.

## Working rules for contributors and agents

- Read this file and both pinned OpenAPI contracts before editing provider behavior.
- Check for a more specific `AGENTS.md` in the directory being modified.
- Keep changes scoped to the current phase and implement only operations defined by the pinned Cloud and Admin contracts.
- Add or update tests with every schema or lifecycle change.
- Run formatting, unit tests, protocol tests, generated-file checks, and `git diff --check` before reporting completion. Report live acceptance validation separately.
- Never use real API keys in fixtures, examples, commits, logs, diagnostics, or issue text.
- Provider code must use the public Cloud/Admin facades and must not import either internal generated package or duplicate generated wire models.
- Treat generated code and docs as generated: modify their source or generator and regenerate them.
- When documentation, implementation, and an OpenAPI contract disagree, investigate against a designated environment, document the discrepancy and support boundary, and add a narrowly scoped facade override/test rather than editing generated code.
- Update the survey date, API versions/provenance/checksums, compatibility table, completed phase checkboxes, and unresolved decisions whenever either snapshot changes.
