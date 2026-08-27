---
page_title: "State and secrets: what this provider stores, and where"
subcategory: "Guides"
description: |-
  Which credentials this provider handles, which of them end up in Terraform state, and what that
  means for how you configure your backend.
---

# State and secrets

This provider handles bearer credentials for two APIs, and some of them are written to Terraform
state. That is not a quirk of this provider — it is how Terraform works — but you need to know which
values, so you can protect the right things.

## The short version

**Terraform state produced by this provider can contain live credentials. Use an encrypted,
access-controlled remote backend, and treat read access to that state as equivalent to read access to
the credentials in it.**

## What goes where

| Value | In state? | Notes |
| --- | --- | --- |
| `management_api_key` (provider) | **No** | Provider configuration only. Sent to `cloud_api_url` and nowhere else. |
| `environment_api_key` (provider) | **No** | Provider configuration only. Sent to `admin_url` and nowhere else. |
| `restate_cloud_api_key.api_key` | **Yes**, sensitive | The only copy. Restate Cloud returns it once, at create. |
| `restate_kafka_cluster.properties` | **Yes**, sensitive | Can contain SASL passwords and TLS key material. |
| `restate_deployment.*.additional_headers` | **Yes**, sensitive | Usually downstream API keys. |
| `restate_cloud_environment.signing_public_key` | Yes | A *public* key. Not a secret. |

Provider configuration is never persisted as resource state. Everything else in the table is, because
Terraform has to remember what it created in order to manage it.

## Why the API key cannot be avoided

`restate_cloud_api_key` exists so an environment's credential can be provisioned declaratively. The
Cloud Management API returns a key's bearer secret **only in the create response** — not from a read,
not from a list, and not from an import. So:

- If the provider did not store it, the key would be unusable and unrecoverable the moment the apply
  finished.
- After `terraform import`, `api_key` is null. Terraform can observe and revoke the key, but it cannot
  tell you the secret. The provider warns about this when you import.
- Any change to the resource replaces the key, which mints a new secret and revokes the old one. Use
  `create_before_destroy` if a running consumer needs an overlap.

If you would rather Terraform never held the secret, mint the key outside Terraform and pass it in
through a variable or `RESTATE_AUTH_TOKEN`. You lose declarative management of the key's lifecycle;
that is the trade.

## Kafka properties and redaction

`restate_kafka_cluster.properties` is a librdkafka property map and can hold `sasl.password`,
`ssl.key.password`, and similar. The Admin API redacts those values when it reads a cluster back, so
the provider cannot simply trust the response:

- For a property whose value the API redacted, or whose **name** marks its value as secret, the
  provider keeps the value from your configuration. The API is not a source of truth for something it
  will not disclose.
- Every other property is compared against the API, so a genuine out-of-band change — someone
  repointing `bootstrap.servers` — still shows up as drift.
- On **import** there is no configured value to fall back on, so state holds the API's redacted
  placeholders. Put the real values in your configuration and apply before relying on the cluster. The
  provider warns about this too.

## Logging

The provider redacts credentials from its own diagnostics and from `TF_LOG` output:

- Errors carry the API's own message, the HTTP status, and a request ID. They deliberately never carry
  the response body or the request headers, because either can contain a bearer token or a user
  payload.
- Where a value could still get through — a transport error quoting a URL, or a server echoing a
  submitted token back in its message — it is passed through a redaction pass that replaces bearer
  credentials and URL userinfo with `[REDACTED]`.

If you find a credential in provider output, that is a vulnerability. Report it as described in
`SECURITY.md`.

Note that Terraform's own trace logging is separate: `TF_LOG=trace` will log the *values* Terraform
passes around, including sensitive ones. Do not attach a trace log to a public issue.

## Practical backend guidance

- Use a remote backend with encryption at rest and a restrictive access policy: S3 with SSE and a
  bucket policy, GCS with CMEK, HCP Terraform, or equivalent.
- Enable state locking so two applies cannot interleave a key replacement.
- Keep the two bootstrap stages in **separate** states. Stage 2 does not need to read stage 1's state,
  and keeping them apart means the state holding the environment API key is not also the state
  everyone reads to find the admin URL. See the two-stage bootstrap guide.
- Never commit `terraform.tfstate`, `*.tfstate.backup`, or a `.tfvars` holding credentials. The
  repository's `.gitignore` covers these; do not override it.
- Rotate `restate_cloud_api_key` with `terraform apply -replace=restate_cloud_api_key.x`. Combine with
  `create_before_destroy` so the consumer is never without a valid credential.

## `sensitive` and what it does not do

Attributes holding secrets are marked sensitive in the schema, which means Terraform redacts them from
plan and apply output and refuses to put them in a non-sensitive `output`. It does **not** encrypt
them, and it does not keep them out of state. Sensitivity is an output-formatting feature; your
backend is the security control.
