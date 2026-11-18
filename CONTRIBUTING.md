# Contributing

## Before you start

Read [AGENTS.md](AGENTS.md). It is both the development plan and the implementation contract: it
records the API-to-Terraform scope, the state and lifecycle rules, the testing strategy, the phase
checklist, and the decisions that are still open. Also read the two pinned OpenAPI contracts under
`pkg/api/` before changing provider behaviour.

## Layout

- `pkg/api/` is the only package that knows either wire contract. It must not import Terraform
  packages, and it must not return `diag.Diagnostics` or framework values.
- `pkg/api/cloud/` and `pkg/api/admin/` are handwritten facades over `internal/generated`
  clients produced by `oapi-codegen`. The facades are the supported Go API; generated code is an
  implementation detail and is never edited by hand.
- `internal/provider/` holds the Terraform schema, state conversion, and resource lifecycle logic.
- `internal/testutil/` holds shared test helpers, including a stateful fake of the Cloud
  Management API's environment and key lifecycles.
- `docs/` is **generated**, in its entirety, from the provider's schema descriptions and the
  configurations in `examples/`. Anything placed directly in `docs/` is deleted on the next
  generation.
- `templates/guides/` holds the handwritten guides, which are copied through verbatim. Contributor
  documentation is not a guide and belongs here in CONTRIBUTING.md.

  Each guide needs YAML frontmatter with `page_title`, `subcategory: "Guides"` and `description`; the
  Terraform Registry uses them, and a guide without them renders untitled.
- `templates/resources.md.tmpl` and `templates/data-sources.md.tmpl` are fallback page templates
  applied to every resource and data source. They reproduce `tfplugindocs`' default rendering
  exactly; the only thing they change is where the example comes from, which is what lets
  `examples/` be flat instead of one directory per page.

  There are deliberately no *per-page* templates. A resource's description, attribute documentation,
  and example already live next to the thing they describe, and a page template would move prose
  away from the code it documents and let the two drift.

  The one exception is `templates/resources/cloud_byok_registration_key.md.tmpl`. The fallback
  template always renders an Import section, and that resource cannot be imported, because the API
  shows a registration key once. The page template is the fallback with that section replaced by one
  sentence, and it has no `.sh` file in `examples/resources/`.
- `examples/resources/` and `examples/data-sources/` are each a single Terraform module whose files
  are named after the page that embeds them: `restate_deployment.tf` is what
  `docs/resources/deployment.md` shows, and `restate_deployment.sh` is its import command. CI
  validates each directory as a whole, so a snippet may reference a resource another file declares
  rather than repeating it.

  Every example describes the same order-processing application. Keep it that way -- the point is
  that the snippets across the reference fit together. Do not pad a documented snippet with a
  resource that exists only to make `terraform validate` pass; put variables in `variables.tf`,
  which is the one file nothing renders. `examples/README.md` has the details.

## Rules

- Keep changes scoped to the current phase in AGENTS.md, and implement only operations that the
  pinned contracts define.
- Add or update tests with every schema or lifecycle change.
- Treat generated code and docs as generated: change the source or the generator, then regenerate.
- When documentation, implementation, and a contract disagree, document the discrepancy and add a
  narrowly scoped facade override plus a test. Never edit generated files.
- Never put a real API key in a fixture, example, commit, log, diagnostic, or issue.

## Checks

Run these before opening a pull request:

```sh
gofmt -l . | grep -v /internal/generated/   # must print nothing
go vet ./...
go test ./...

# Generated API clients and documentation must be up to date.
go generate ./pkg/api/...
go generate ./tools/...
terraform fmt -check -recursive examples/
git diff --exit-code
git diff --check
```

On macOS, documentation generation runs Terraform, which launches the provider over a Unix domain
socket under `TMPDIR`. macOS caps a socket path at 104 bytes, so a long `TMPDIR` produces a
misleading "failed to negotiate the initial go-plugin protocol handshake". Use a short one:

```sh
TMPDIR=/tmp/tfdocs go generate ./tools/...
```

Report live acceptance-test results separately from the checks above; they need real credentials and
a designated test account. They are gated twice over: `TF_ACC=1`, the credentials for the API under
test, and `RESTATE_ACC_ALLOW_DESTRUCTIVE=1` for anything that creates or deletes real objects.

## Tests

Three layers, in increasing cost:

- **Client contract tests** (`pkg/...`) exercise each facade against `httptest` fixtures: wire shape,
  bearer authentication, path escaping, optional and absent fields, error bodies, retry
  classification, and non-leakage of secrets into diagnostics.
- **Terraform protocol tests** (`internal/...`) use `terraform-plugin-testing` and run without
  credentials. They download Terraform on first run. Prefer `internal/testutil.CloudFake` over a queue
  of canned responses for anything with a lifecycle: a queue cannot express "after a destroy is
  accepted, describing the object reports it gone", and a test that tries turns the provider's correct
  decision to poll into a timeout.
- **Acceptance tests** (`TestAcc*`) talk to real APIs.

Every test asserts a behaviour and says why in a comment. A test whose comment is a restatement of its
own name is not pulling its weight.
