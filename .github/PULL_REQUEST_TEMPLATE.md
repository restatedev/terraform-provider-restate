## What this changes

<!-- One or two sentences. If it changes a resource schema or a lifecycle behaviour, say which. -->

## Why

<!-- What was wrong, or what the API now supports that it did not before. -->

## Checks

- [ ] `gofmt -l . | grep -v /internal/generated/` prints nothing
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go generate ./pkg/api/...` produces no diff
- [ ] `go generate ./tools/...` produces no diff (docs are generated)
- [ ] `terraform fmt -check -recursive examples/`

## If this touches a schema or a lifecycle

- [ ] A test asserts the new behaviour, and its comment says why the behaviour is correct
- [ ] The plan after apply is empty, and after import is empty
- [ ] Any destructive path is behind a named, documented opt-in
- [ ] Any new secret-bearing attribute is marked sensitive, and the state guide is updated
- [ ] `AGENTS.md` phase checkboxes and unresolved decisions are updated

## If this refreshes an OpenAPI snapshot

- [ ] The snapshot's `PROVENANCE.md` records the new checksum, version, revision and date
- [ ] `CHANGELOG.md` notes any change that affects a Terraform schema

## Acceptance tests

<!-- Acceptance tests need real credentials and never run in PR CI. State whether you ran them, and
     against which account or environment. "Not run" is a fine answer for a docs-only change. -->
