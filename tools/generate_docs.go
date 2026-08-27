// Package tools holds the repository's build-time tooling entry points.
//
// Documentation under docs/ is generated from the provider's schema descriptions and the runnable
// configurations under examples/. Regenerate it with `go generate ./tools/...`, and commit the
// result; CI fails on a diff.
//
// Handwritten guides under docs/guides/ are not generated and are preserved: the generator copies
// them through. The generated reference is not a substitute for them, and in particular not for the
// two-stage bootstrap and state-security guidance.
//
// Note for macOS: generation runs Terraform, which launches the provider over a Unix domain socket
// created under TMPDIR. macOS caps a socket path at 104 bytes, so a long TMPDIR makes the provider
// fail to start and the generator reports an unhelpful "failed to negotiate the initial go-plugin
// protocol handshake". If that happens, use a short TMPDIR:
//
//	TMPDIR=/tmp/tfdocs go generate ./tools/...
package tools

// go:generate runs with this file's directory as the working directory, so the generator is pointed
// at the repository root explicitly; without it, it looks for examples/ and docs/ under tools/.
//go:generate go tool tfplugindocs generate --provider-dir .. --provider-name restate --rendered-provider-name "Restate"
