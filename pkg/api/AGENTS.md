# API layer

This package and its subpackages are the only place in the provider that knows either wire contract.
Read the repository's `AGENTS.md` first; the rules below are the ones specific to this layer.

## Boundaries

- **No Terraform.** This package must not import a Terraform package, and must not return
  `diag.Diagnostics`, framework values, or resource state models. It returns typed domain values and
  typed errors.
- **Generated code is internal.** `pkg/api/cloud/internal/generated` and
  `pkg/api/admin/internal/generated` are implementation details. The handwritten facades are the
  supported Go API, and their domain types exist so a regenerated client cannot push a field rename
  into a Terraform schema.
- **Generated code is never edited.** When the contract and reality disagree, add a narrowly scoped
  override in the facade with a test that documents the discrepancy.
- **Credentials never cross.** Each facade builds its own client from its own credential. There is no
  shared client that could be handed the wrong token, and that is deliberate rather than incidental.

## Retry safety is a property of the operation

`api.RetrySafety` is passed explicitly at every call site, and the zero value is the safest one. The
levels are not a performance tuning knob:

- `RetryThrottleOnly` retries only responses that prove the request was rejected without being acted
  on: 429, and 503 with `Retry-After`. **Every create uses this.** A 5xx from a create may mean the
  object exists, and a retry would create a second one.
- `RetryServerErrors` additionally retries retryable 5xx. Only for an operation that is idempotent by
  construction — one that sets an absolute value, or a delete.
- `RetryTransportErrors` additionally retries a failure with no response at all, where the provider
  cannot tell whether the server saw the request. **Reads only.**

If you add an operation, state its safety and say why in a comment. "It's probably fine" is how a
duplicate environment gets created.

## Errors

- Never put a response body or a request header in an error. Either can contain a bearer token, a
  Kafka password, or a user payload. `Error` has no field for them on purpose.
- Do carry the status, the API's own message, its error code, and the request ID. The request ID is
  the difference between an actionable support request and a screenshot.
- A message that could contain an echoed credential goes through `RedactString`. Each facade's error
  decoder already does this.
- Collapse the API's two ways of saying "gone" — a 404 and a tombstone status — into one sentinel
  error per object kind, so the provider layer has a single condition to check.

## Capability interfaces

Each endpoint adapter asserts, at compile time, exactly the capabilities its endpoint supports. Those
assertions are documentation that cannot go stale: subscriptions and Cloud API keys have no `Updater`
because their contracts have no update operation, and a future attempt to add one will not compile
until the contract does.

Do not add a capability an endpoint does not have in order to make the set look symmetrical. Where an
endpoint's mutation genuinely does not fit — Cloud environments split rename and tier change across
two operations with different billing semantics — prefer explicit methods over one `Update` that hides
the risky half.

## Contract snapshots

- Both snapshots are committed pristine and are never edited. Each has a `PROVENANCE.md` recording
  source, revision, advertised version, checksum, and refresh procedure, and
  `pkg/api/contracts/contracts_test.go` fails if a snapshot changes without its record being updated.
- Regenerate with `go generate ./pkg/api/...`. CI fails on a diff.

## Testing

Contract-test every method against `httptest` fixtures: wire shape, bearer authentication, path
escaping, query parameters, request bodies, success responses, structured errors, malformed bodies,
absent optional fields, and cancellation. The generic helpers in `operations.go`, `retry.go` and
`poll.go` are unit-tested once with synthetic types; do not re-test them through every endpoint.

Two properties are worth asserting for every write: that a field the caller did not configure is
**absent** from the request rather than sent as a zero value, and that no failure path puts a secret
into the error string.
