# Provider layer

This package owns Terraform schema, state conversion, and resource lifecycle logic. It reaches the
two APIs only through the facades in `pkg/api`, and it must not import either generated client.

Read the repository's `AGENTS.md` first. The rules below are the ones specific to this layer.

## The distinctions that matter

**Null, empty, and unknown are three different things.** A null attribute means "not configured or
not reported"; an empty map or list means "the API answered and there was nothing"; unknown means
"not decided yet". Collapsing any two of them produces a resource that never converges. Concretely:

- An optional string the API does not report is `types.StringNull()`, not `types.StringValue("")`.
  `optionalString` exists for this.
- A computed map is an empty map, not null: the API answered. `orEmptyStringMap` exists for this.
- A `*bool` from the facade becomes `types.BoolNull()` when nil, so "the API said false" stays
  distinguishable from "the API said nothing". `optionalBool` exists for this.
- A nil map sent to the API means "leave this alone"; an empty map means "set this to no entries".
  The facades preserve that distinction, so do not normalize one into the other on the way down.

**Never replace a value the API will not disclose.** A create-only secret, or a property the API
redacts, must keep what state already holds. `Read` on `restate_cloud_api_key` captures `api_key`
before applying the response and puts it back; `restate_kafka_cluster` and the deployment resources
merge through `admin.MergeRedactedProperties`. Overwriting one of these destroys the only copy of a
live credential.

**An attribute the configuration does not manage stays out of state.** `restate_service_configuration`
sets an attribute from the API only when it is already non-null, because adopting an unmanaged value
would put it in the next plan.

## Lifecycle rules

- Every resource implements create, read, update where the API supports it, delete, and import.
- A 404 during refresh removes the resource from state. A 404 during delete is success. Where the API
  also reports a tombstone status, both count as absence — the facades collapse them into a single
  sentinel error for exactly this reason.
- A create that fails after the API assigned an identifier must record that identifier in state, so
  Terraform can destroy the object rather than leaving an orphan nobody knows about.
- An update that fails part way must re-read, so state reflects what was applied rather than what was
  planned.
- Prefer re-reading after a write over assembling state from the write's response: the read path is
  what the next refresh sees, so state must describe that.
- Where the API cannot apply a change, use `RequiresReplace` — never silently drop the field. Where
  the change is genuinely create-time-only and replacement would be disproportionate (`force`,
  `breaking`), warn explicitly.

## Diagnostics

A diagnostic is read by someone who is stuck. Name the thing, say what the API said, and say what to
do about it. Compare:

    Error: unexpected status 403

    Error: Could not authenticate to the Restate Cloud Management API
    The configured management API key cannot read account acc_1a2b3c: check that the key is
    account-scoped with role rst:role::ManagementApiAccess and that it belongs to this account, not
    another one

Where a resource cannot proceed because of a provider-configuration gap, use `requireCloudClient` or
`requireAdminClient`: they name the attributes to set, the environment variables, and the alias
mechanism.

Never put a credential in a diagnostic. Errors from `pkg/api` already carry no response body and no
headers; do not add them back.

## Testing

Protocol tests use `terraform-plugin-testing` and run without credentials.

Use `internal/testutil.CloudFake` for anything with a lifecycle, rather than a queue of canned
responses. A queue cannot express "after a destroy is accepted, describing the object reports it
gone", and a test that tries turns the provider's correct decision to poll into a test that waits out
its timeout — which is how these tests originally took 95 minutes instead of 95 seconds.

Two harness constraints worth knowing before you spend time on them:

- `PlanOnly` cannot be combined with `ConfigPlanChecks.PreApply`. Let the step apply.
- The harness serves every provider configuration from one in-process server, so data sources
  declared against different aliases all see the most recently configured client. Alias behaviour can
  be asserted at configure time, not at read time.

Assert the behaviour and say why it is correct. A test comment that restates the test's own name is
not pulling its weight.
