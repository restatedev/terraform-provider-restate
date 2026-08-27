# Cloud Management API contract provenance

| Field | Value |
| --- | --- |
| File | `openapi.yaml` |
| Source | `generated/schema/openapi.yaml` in the `restatedev/restate-cloud` repository |
| Upstream revision | `d9886ea57b88a40edd468bf48a9ffe0a294dee54` |
| Retrieved | 2026-08-27 |
| Advertised `info.version` | `v1` |
| OpenAPI version | 3.1.0 |
| SHA-256 | `5440b3ff73d889f28466cd117e00c922d7a63f69dc7d9d913d4e6e69e8e0f09f` |
| License | Proprietary; vendored with permission as the contract this provider is built against. |

The snapshot is committed pristine and is never edited.

## Normalization

The upstream contract templates `{accountId}` into account-scoped paths but never declares the
corresponding path parameter, which makes the document invalid — code generators either reject it
or silently drop the parameter. `tools/cmd/normalizecloudspec` injects the missing declaration and
writes `openapi.normalized.yaml`. That file is generated, committed, and diff-checked; the
generator consumes it instead of the pristine snapshot so the snapshot keeps its provenance and
checksum.

The normalization is mechanical and narrow: it only adds an `accountId` path parameter to an
operation whose path template contains `{accountId}` and which does not already declare one. Any
other difference between the two documents is a bug in the tool, and
`tools/cmd/normalizecloudspec/main_test.go` asserts as much.

## Generator

| Field | Value |
| --- | --- |
| Generator | `github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen` |
| Version | pinned as a Go tool dependency in `go.mod` |
| Config | `oapi-codegen.yaml` |
| Output | `internal/generated/cloud.gen.go` |

Regenerate with `go generate ./pkg/api/...`. The output is committed; CI fails on a diff.

## Compatibility and upgrade policy

The Cloud Management API has no version endpoint and no published deprecation policy, so the
pinned `v1` snapshot recorded above **is** the provider's compatibility boundary. The provider
therefore:

- Sends only the operations this snapshot defines, and never probes for newer ones.
- Ignores unknown response fields rather than failing, so an additive server change cannot break
  an existing configuration.
- Fails with an actionable diagnostic — not a partial apply — when a response omits a field the
  snapshot marks required.
- Validates credentials with the read-only `DescribeAccount` operation, since there is nothing
  else to negotiate against.

A refreshed snapshot that changes or removes a field the provider reads is a breaking change for
the provider and requires a minor or major release, a `CHANGELOG.md` entry, and upgrade notes —
even when the API itself considers the change compatible.

## Refreshing this snapshot

1. Copy the reviewed upstream `generated/schema/openapi.yaml` over `openapi.yaml`.
2. Update the upstream revision, retrieval date, advertised version, and SHA-256 above.
3. Run `go generate ./pkg/api/...` and `go test ./...`.
4. Diff the normalized document to confirm the normalization is still needed and still narrow.
5. Record schema-affecting changes in `CHANGELOG.md`, and update the survey date and unresolved
   decisions in `AGENTS.md`.
