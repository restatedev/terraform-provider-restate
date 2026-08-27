// Package cloud is the supported Go API for the Restate Cloud Management API.
//
// The wire contract is pinned at openapi.yaml; see PROVENANCE.md for its origin, advertised
// version, and checksum. `go generate ./pkg/api/...` normalizes that document and regenerates the
// typed client into internal/generated. Both the normalized document and the generated client are
// committed, and CI fails if regenerating them produces a diff.
package cloud

//go:generate go run github.com/restatedev/terraform-provider-restate/tools/cmd/normalizecloudspec -in openapi.yaml -out openapi.normalized.yaml
//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.normalized.yaml
