# Admin API contract provenance

| Field | Value |
| --- | --- |
| File | `openapi.json` |
| Source | <https://docs.restate.dev/schemas/openapi-admin.json> |
| Retrieved | 2026-08-27 |
| Advertised `info.version` | `1.7.6` |
| OpenAPI version | 3.1.0 |
| SHA-256 | `5361f4818c71242f7ff1c7e4d2d40efaefae4de998e7493065d5bd5de2e8bd06` |
| License | MIT (`info.license` in the document) |

The snapshot is committed pristine: byte-for-byte as published. It is never edited. Discrepancies
between the contract and the live API are handled by narrowly scoped overrides in the facade, with
a test that documents the discrepancy.

## Generator

| Field | Value |
| --- | --- |
| Generator | `github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen` |
| Version | pinned as a Go tool dependency in `go.mod` |
| Config | `oapi-codegen.yaml` |
| Output | `internal/generated/admin.gen.go` |

Regenerate with `go generate ./pkg/api/...`. The output is committed; CI fails on a diff.

## Version negotiation

`GET /version` reports `min_admin_api_version` and `max_admin_api_version`. The provider requires
its supported range to overlap the server's; see `pkg/api/admin/version.go` for the supported
range and the compatibility policy.

## Refreshing this snapshot

1. Re-download the document and replace `openapi.json`.
2. Update the retrieval date, advertised version, and SHA-256 above.
3. Run `go generate ./pkg/api/...` and `go test ./...`.
4. Record any contract change that affects a Terraform schema in `CHANGELOG.md`, and update the
   survey date and unresolved decisions in `AGENTS.md`.
