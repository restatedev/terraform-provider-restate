---
page_title: "Destructive operations and the guards around them"
subcategory: "Guides"
description: |-
  Every operation in this provider that can lose data or break running work, why it is guarded, and
  how to proceed deliberately when you mean to.
---

# Destructive operations

Several Restate operations cannot be undone, and a few can break work that is currently running. Each
is behind an explicit, named opt-in rather than happening as a side effect of an apply. This guide
lists them all in one place.

## Destroying an environment

`terraform destroy` on a `restate_cloud_environment` deletes the environment **and its data**. There is
no undo and no export.

**The guard:** `deletion_protection`. When true, destroy is refused before any request reaches the API.

```hcl
resource "restate_cloud_environment" "production" {
  name                = "production"
  deletion_protection = true
}
```

To tear one down deliberately, clear the guard, apply that change, and then destroy. That is two
applies on purpose: an accidental destroy cannot also clear its own protection.

The default is `false`, so a throwaway environment does not need ceremony. Set it on anything you
would be upset to lose.

## Changing an environment's region

The Cloud Management API has no operation that moves an environment between regions. Changing `region`
therefore **replaces** the environment: Terraform destroys the old one — data included — and creates a
new one.

**The guard:** nothing implicit. Terraform's plan shows a replacement, and `deletion_protection` still
applies to the destroy half. Read your plans.

Use the `restate_cloud_regions` data source to validate a region before you apply, so a typo fails at
plan time instead of becoming a replacement.

## Replacing an API key

Any change to a `restate_cloud_api_key` — role, environment, even the description — replaces it,
because the API has no update operation. Replacement mints a new secret and **revokes the old one**,
which breaks anything still authenticating with it.

**The guard:** Terraform's plan shows the replacement. To avoid a gap:

```hcl
resource "restate_cloud_api_key" "ingress" {
  environment_id = restate_cloud_environment.production.id
  role_id        = "rst:role::IngressAccess"

  lifecycle {
    create_before_destroy = true
  }
}
```

## Deleting a deployment

The Admin API implements only **forced** deletion of a deployment. A forced delete can leave in-flight
invocations in an unrecoverable error state.

**The guard:** `force_destroy`. Terraform refuses to destroy the resource until it is set *and
applied*.

```hcl
resource "restate_deployment" "orders" {
  http {
    uri = "https://orders.internal.example.com:9080"
  }

  force_destroy = true
}
```

The usual reason to delete a deployment is that a newer one has superseded it. Prefer draining first:
stop routing new work to it and let existing invocations finish, then delete.

## Registering a deployment with `force` or `breaking`

- `force` overrides an existing deployment at the same URI or ARN, and **can put in-flight invocations
  into an unrecoverable state**. It implies `breaking`.
- `breaking` allows a service revision whose schema is incompatible with the previous one, including a
  change of service type.

Both are registration behaviour: they apply when the deployment is created, and the API neither accepts
nor reports them on an update. Changing one on an existing resource does nothing to that deployment,
and the provider emits a warning saying so rather than letting you believe otherwise.

## Updating a deployment with `overwrite`

Without `overwrite`, updating a deployment's endpoint does **not** rediscover its schema: the
deployment keeps the services and handlers it was registered with. With `overwrite`, the update
rediscovers and **accepts breaking schema changes**.

Use it when the endpoint's schema has genuinely changed and you accept the consequences. Note that it
can also reset `restate_service_configuration` settings back to what the SDK reports — see below.

## Deleting a Kafka cluster

Deleting a cluster that subscriptions still reference **orphans those subscriptions**.

**The guard:** by default the provider does not send the API's `force` parameter, so the server's own
dependency check refuses the delete and the provider explains why. Prefer deleting the subscriptions
first; Terraform will order that correctly if the subscription references the cluster.

`force_destroy = true` skips the check. Only use it when you have decided the orphaned subscriptions do
not matter.

## Removing a service configuration

This one is the opposite of destructive, and it surprises people: `terraform destroy` on a
`restate_service_configuration` **does not revert anything**.

The Admin API has no operation that resets a service setting to the value discovery gave it, and the
contract does not define what a JSON null means for those fields. So the provider stops managing the
settings and leaves the applied values in place, and warns that it has done so. The same applies to
removing an individual attribute from the resource.

To restore SDK-discovered defaults, re-register the deployment with `overwrite = true`.

## Operations this provider deliberately does not offer

These are runtime operations, not infrastructure, and representing them as Terraform resources would
make them replayable by an apply:

- Cancelling, killing, pausing, resuming, purging, or restarting an invocation.
- Mutating Virtual Object state, which is application data.

Use the Restate CLI or the Admin API directly for those.

## Summary

| Operation | Guard | Default |
| --- | --- | --- |
| Destroy an environment | `deletion_protection` | Permitted |
| Change an environment's region | Plan shows a replacement | Permitted |
| Change anything on an API key | Plan shows a replacement | Permitted |
| Delete a deployment | `force_destroy` | **Refused** |
| Override a deployment at the same URI | `force` | Off |
| Allow a breaking schema on registration | `breaking` | Off |
| Rediscover schema on update | `overwrite` | Off |
| Delete a Kafka cluster with subscriptions | `force_destroy` | **Refused by the server** |
| Revert a service configuration | Not possible | Warns |
