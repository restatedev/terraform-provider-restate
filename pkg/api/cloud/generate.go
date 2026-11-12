// Package cloud is the supported Go API for the Restate Cloud Management API.
//
// The wire contract is pinned at openapi.yaml; see PROVENANCE.md for its origin, advertised
// version, and checksum. `go generate ./pkg/api/...` regenerates the typed client into
// internal/generated from that document. The generated client is committed, and CI fails if
// regenerating it produces a diff.
package cloud

//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml
