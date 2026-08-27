// Package admin is the supported Go API for the Restate per-environment Admin API.
//
// The wire contract is pinned at openapi.json; see PROVENANCE.md for its origin, advertised
// version, and checksum. `go generate ./pkg/api/...` regenerates the typed client into
// internal/generated. The generated client is committed, and CI fails if regenerating it produces
// a diff.
package admin

//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.json
