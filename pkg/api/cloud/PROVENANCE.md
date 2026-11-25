# Cloud Management API contract provenance

| Field | Value |
| --- | --- |
| File | `openapi.yaml` |
| Source | `generated/schema/openapi.yaml` in the `restatedev/restate-cloud` repository |
| Upstream revision | `848391c44ac79523e7796c506e368535ccaa428b`, the head of restatedev/restate-cloud#2939 (`bh/byoc-region-access-api`), which is open and not merged |
| Retrieved | 2026-11-25 |
| Advertised `info.version` | `v1` |
| OpenAPI version | 3.1.0 |
| SHA-256 | `3aabddd9219649a05593432a5936bef47e39df2472e5f9786aa57e0172cbe053` |
| License | Proprietary; vendored with permission as the contract this provider is built against. |

The snapshot is committed pristine and is never edited.

The revision is a pull request head, not a commit on the upstream main branch. Replace the snapshot with the
`generated/schema/openapi.yaml` of the merge commit of restatedev/restate-cloud#2939 before the provider is released, and
record that commit here. Compared with the previous snapshot (`84a5a2144a1877d326c2cad89713bc59d37b2e4d`), it adds the
`SetRegionAccess` operation with its `SetRegionAccessRequest` and `SetRegionAccessResponse` schemas, and the optional
`access` object (`RegionAccess`, `RegionEndpointService`) of `RegionDescription`. It changes no other operation and no
required field. It also carries main's later wording changes to the idempotency-key descriptions of `CreateWebhook` and
`DescribeWebhook`, which the provider does not use.

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
