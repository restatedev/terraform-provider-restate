# Contributing

## Before you start

Read [AGENTS.md](AGENTS.md). It is both the development plan and the implementation contract: it
records the API-to-Terraform scope, the state and lifecycle rules, the testing strategy, the phase
checklist, and the decisions that are still open. Also read the two pinned OpenAPI contracts under
`pkg/api/` before changing provider behaviour.

## Layout

- `pkg/api/` is the only package that knows either wire contract. It must not import Terraform
  packages, and it must not return `diag.Diagnostics` or framework values.
- `pkg/api/cloud/` and `pkg/api/admin/` are handwritten facades over `internal/generated`
  clients produced by `oapi-codegen`. The facades are the supported Go API; generated code is an
  implementation detail and is never edited by hand.
- `internal/provider/` holds the Terraform schema, state conversion, and resource lifecycle logic.
- `internal/testutil/` holds shared test helpers.

## Rules

- Keep changes scoped to the current phase in AGENTS.md, and implement only operations that the
  pinned contracts define.
- Add or update tests with every schema or lifecycle change.
- Treat generated code and docs as generated: change the source or the generator, then regenerate.
- When documentation, implementation, and a contract disagree, document the discrepancy and add a
  narrowly scoped facade override plus a test. Never edit generated files.
- Never put a real API key in a fixture, example, commit, log, diagnostic, or issue.

## Checks

Run these before opening a pull request:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./...
go generate ./pkg/api/... && git diff --exit-code   # generated files must be up to date
git diff --check
```

Report live acceptance-test results separately from the checks above; they need real credentials
and a designated test account.
