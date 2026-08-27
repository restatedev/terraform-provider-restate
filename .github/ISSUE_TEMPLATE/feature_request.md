---
name: Feature request
about: A resource, data source, or attribute the provider should support
labels: enhancement
---

## What you want to manage

## Which API operation backs it

<!-- The provider only implements operations defined by its two pinned OpenAPI contracts, under
     pkg/api/*/. If the operation is not in one of them, say so -- it may need an upstream API change
     first. -->

## Why it belongs in Terraform

<!-- Terraform manages convergent infrastructure. Some Restate operations deliberately have no
     resource: cancelling or purging an invocation, mutating Virtual Object state. Those are runtime
     actions, and an apply would replay them. See the "Explicitly deferred or excluded" section of
     AGENTS.md. -->

## Lifecycle

<!-- For a new resource, the parts that decide whether it can be shipped safely:
     - Can it be read back? An attribute the API does not report cannot be refreshed or imported.
     - Can it be updated in place, or does a change have to replace the object?
     - What does deleting it do, and can that break running work?
     - Does it carry a secret? Can that secret be read back after creation? -->

## Anything else
