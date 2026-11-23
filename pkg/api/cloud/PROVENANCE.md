# Cloud Management API contract provenance

| Field | Value |
| --- | --- |
| File | `openapi.yaml` |
| Source | `generated/schema/openapi.yaml` in the `restatedev/restate-cloud` repository |
| Upstream revision | `84a5a2144a1877d326c2cad89713bc59d37b2e4d`, the tip of `main` when the snapshot was refreshed |
| Retrieved | 2026-11-23 |
| Advertised `info.version` | `v1` |
| OpenAPI version | 3.1.0 |
| SHA-256 | `99cd77e0709e6793e4e58e2cc986d66d05026f673a2f4f3c7ecac99b106cba47` |
| License | Proprietary; vendored with permission as the contract this provider is built against. |

The snapshot is committed pristine and is never edited.

The revision is on the upstream main branch. The file was last changed there by `407e761110c513b1dffd43188c6f30a1b6ce6e48`
(restatedev/restate-cloud#2859). Compared with the previous snapshot (`91acbf0590b7fdef5bf641d4ed9075cd514c5d05`), it adds
the optional `imagePull` object to the `CreateByokRegistrationKey` response, which carries the registry proxy credential of
the region the key reserves. It changes no operation and no required field.

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
