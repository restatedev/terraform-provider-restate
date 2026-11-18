# Cloud Management API contract provenance

| Field | Value |
| --- | --- |
| File | `openapi.yaml` |
| Source | `generated/schema/openapi.yaml` in the `restatedev/restate-cloud` repository |
| Upstream revision | `00e44d37d34516e6f4c8cec72f984d3d1b335655`, the tip of the unmerged branch `bh/byok-concierge` |
| Retrieved | 2026-11-18 |
| Advertised `info.version` | `v1` |
| OpenAPI version | 3.1.0 |
| SHA-256 | `0c09055e1a4f17a9fcf9c5a9e578b0b478cfc2386a964e41c64c9e6eb5d79b3e` |
| License | Proprietary; vendored with permission as the contract this provider is built against. |

The snapshot is committed pristine and is never edited.

The revision is not on the upstream main branch yet. It differs from the previous snapshot
(`ea764f52897028cb6b6f2763e092f23c90e990f0`) only by the BYOK operations `CreateByokRegistrationKey` and
`DeleteByokRegion` with their schemas, and the response of `CreateByokRegistrationKey` carries the `regionId`
only on this branch. Pin the merge commit of that branch and regenerate once it is on main.

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
4. Record schema-affecting changes in `CHANGELOG.md`, and update the survey date and unresolved
   decisions in `AGENTS.md`.
