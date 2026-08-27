---
name: Bug report
about: Something the provider does that it should not, or does not do that it should
labels: bug
---

## What happened

<!-- Include the Terraform output. Redact credentials before pasting: a plan or apply can echo
     values, and TF_LOG=trace logs sensitive values too. -->

## What you expected

## Configuration

<!-- The smallest configuration that reproduces it. Replace real IDs, URLs and keys with
     placeholders. -->

```hcl

```

## Versions

- Provider version:
- Terraform version (`terraform version`):
- Restate version, if this involves the Admin API (`data.restate_server_info.x.version`):
- Restate Cloud: yes / no. If self-hosted, note that compatibility is not yet advertised.

## Which API

- [ ] Cloud Management API (environments, API keys)
- [ ] Admin API (deployments, Kafka clusters, subscriptions, service configuration)
- [ ] Provider configuration
- [ ] Not sure

## Anything else

<!-- If a plan is never empty, or a refresh keeps changing something, say which attribute. If an
     error mentions a request ID, include it -- it is the fastest route to the server-side log. -->
