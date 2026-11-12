# Azure

This module creates an Entra app registration that Restate Cloud uses to build the region in your Azure subscription.

## What it creates

| Resource                                  | Purpose                                                                                                                                                                                                                                |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The resource group `restate-byoc-region`  | The region lives in this group. For an older region, it is tagged `restate-cloud-region` with the region's tag, and Restate Cloud builds nothing until the tag matches. A new region needs no tag. Region grant mode creates no group. |
| The app registration `Restate Cloud BYOC` | Restate Cloud builds the region as this application. Its federated credentials accept sign-ins from the region's own identity, or, for an older region, from Restate Cloud's own identities only.                                      |

## How Restate Cloud signs in

Restate Cloud signs in through OpenID Connect (OIDC) federation. It presents a short-lived token, signed by its OIDC issuer, and Entra ID checks the token against the issuer's published keys, then exchanges it for a short-lived access token of the app registration. No client secret or certificate exists.

Restate decides which token a region presents when you create the region. With `restate`, the module follows what Restate Cloud answered for the region.

### With the region's own identity

A new region presents a token from Restate's issuer whose subject names the region. The app registration has one federated credential, which accepts a token only when all of these hold:

| Check    | Accepted value                                                                                           |
| -------- | -------------------------------------------------------------------------------------------------------- |
| Issuer   | Restate's issuer, `restate_issuer`. With `restate`, the module takes it from the region.                 |
| Audience | `api://AzureADTokenExchange`.                                                                            |
| Subject  | Exactly the region's subject, `region:<region-id>`. With `restate`, the module takes it from the region. |

### Through the composition cluster

A region created before regions had their own identity presents a token that names one of Restate's identities as its subject. Each identity gets its own federated credential on the app registration, which accepts a token only when all of these hold:

| Check    | Accepted value                                                                                     |
| -------- | -------------------------------------------------------------------------------------------------- |
| Issuer   | Restate Cloud's issuer, `composition_issuer`. With `restate`, the module takes it from the region. |
| Audience | `api://AzureADTokenExchange`.                                                                      |
| Subject  | Exactly one identity from `providers_service_accounts`. Prefixes are never accepted.               |

## What Restate Cloud can do with the app registration

The app registration is Contributor on the whole subscription. A narrower scope is not possible, because AKS creates the cluster's node resources in a resource group of its own, which must not exist before the cluster does. This is the reason the subscription must hold nothing except the region.

Contributor cannot create role assignments, and an app registration belongs to the tenant and sits outside the subscription. So the grant cannot change its own access or its own federated credentials.

## Region grant mode

Region grant mode serves a subscription that will hold many Restate regions, for example when you build a region for each of your own customers. The module runs once for the subscription. It creates a grant that belongs to your Restate account or organization, and it builds the regions you list in `region_grant.regions`. Administrators of that account or organization can also build regions with the grant elsewhere, with `restate_cloud_byoc_region` or with `CreateRegion` naming the grant, and nothing changes in your subscription for each new region.

Set `region_grant` to turn the mode on. It excludes `restate` and the manual inputs, and Terraform refuses to plan a configuration that combines them.

In this mode the module makes its Restate Cloud calls through the `restatedev/restate` provider, with your `provider "restate"` configuration, so it takes no `restate_api_key` or `restate_access_token`. The provider's credential decides who the grant belongs to. For owner `ACCOUNT`, use an account key with the Management API role or an account administrator's token. For owner `ORGANIZATION`, use an organization administrator's token or an organization key with the `OrganizationRegionGrantAccess` role, and set the provider's `account_id` to any account of the organization.

### What it creates

- A region grant in Restate Cloud, which `restate_cloud_region_grant` creates with `CreateRegionGrant`. Restate answers with the issuer, the grant's subject `grant:<grant-id>`, the token audience and the resource prefix, and the module builds the rest from these.
- The app registration `Restate Cloud BYOC` with one federated credential. It accepts a token only from Restate's issuer, with the audience `api://AzureADTokenExchange` and exactly the subject `grant:<grant-id>`.
- The Contributor role on the subscription, which is the same role a region-scoped grant holds.

The module creates no resource group, because the composition creates one for every region, named `rgr-<10 characters of the grant id>-<10 characters of the region id>`. Contributor on the subscription allows this. Every resource a region creates carries the grant's prefix, which the `resource_prefix` output shows. Regions of one grant can reach each other's resources, because they share the identity and the subscription. Put regions that need a hard boundary into separate subscriptions.

A subscription can have one confirmed region grant. Confirming a second one fails with 409, whichever account or organization owns the first.

### Inputs

| Variable          | Description                                                                                                                                                                                                                                                                                   |
| ----------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `region_grant`    | The grant to create. `owner` is `ACCOUNT` by default, or `ORGANIZATION`. `display_name` names the grant in Restate Cloud, and changing it replaces the grant. `regions` lists the regions to build with the grant, by name, each with an optional `cloud_region`, `display_name`, `machine_type` and `deletion_protection`, which is true by default and must be set to false before the region can be deleted. |
| `subscription_id` | The subscription that holds the regions. It must be dedicated to them.                                                                                                                                                                                                                        |
| `tenant_id`       | The Entra tenant of the subscription. The default is the tenant of the credentials Terraform runs with.                                                                                                                                                                                       |
| `location`        | Optional. The location of a listed region that sets no `cloud_region`. A region with neither is refused.                                                                                                                                                                                      |

`resource_group` has no effect in this mode, because the composition creates a resource group for each region. Restate must have enabled BYOC for the owning account or organization before it accepts `CreateRegionGrant`, and the call otherwise answers 403.

### Example

```hcl
provider "restate" {} # RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY

module "restate_byoc" {
  source = "github.com/restatedev/terraform-restate-cloud//modules/byoc/azure"

  subscription_id = "00000000-0000-0000-0000-000000000000"
  region_grant = {
    owner        = "ORGANIZATION"
    display_name = "Azure production"
    regions      = { prod-eu = { cloud_region = "swedencentral" } }
  }
}
```

The outputs `grant_id`, `resource_prefix`, `status`, `region_ids` and `grant` describe the grant and its regions. The outputs of a single region are empty in this mode.

### Confirming the grant

After the app registration, its federated credential and the role assignment exist, `restate_cloud_region_grant_confirmation` calls `ConfirmRegionGrant`. Restate then signs in through the grant and reads the subscription, and it answers with the cloud's own message when either step fails. Entra ID and Azure Resource Manager need time to see a new federated credential and a new role assignment. Microsoft gives no fixed figure for the credential and documents up to ten minutes for a role assignment, so Restate keeps signing in in the background, and the apply waits up to 15 minutes for the grant to become `READY` before it reports the cloud's last answer.

A refused confirmation leaves the grant `PENDING` for seven days. Run `terraform apply` again, and the module confirms the same grant without creating another one. Once the grant is `READY`, the listed regions are built with it, and the apply waits until each one is `ACTIVE`.

### A grant that ended

A grant can end outside Terraform, for example when an administrator deletes it or when it expires. A refresh then notices it, and the next apply creates a new grant, updates the federated credential for it and confirms it.

### Destroying the grant

`terraform destroy` deletes the listed regions first and waits until Restate has removed what it built. It then calls `DeleteRegionGrant` and removes the app registration and the role only after Restate has answered. Restate answers 409 while any region still uses the grant, including regions that other configurations built with it, so the destroy stops and the grant stays in your subscription. Delete those regions and run the destroy again.

### Changing a grant

A grant keeps the settings it was created with. A change to `owner`, `display_name` or `subscription_id` replaces the grant and its confirmation together, and Restate refuses the replacement while a region uses the grant. When regions exist, delete them before you change these inputs.

## Inputs

| Variable               | Description                                                                                                                                                                                                                                                                                                 |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `subscription_id`      | The subscription the region is built in.                                                                                                                                                                                                                                                                    |
| `location`             | The region's location, for example `westeurope`. With `region_grant`, the location of a listed region that sets no `cloud_region`.                                                                                                                                                                          |
| `restate`              | Your Restate Cloud account and the region: `account_id`, `name`, and optionally `display_name`, `machine_type`, `api_url` and `region_identity`. Set `region_identity = false` for a new region while Restate Cloud gives no region its own identity in production, and leave it unset for an existing one. |
| `restate_api_key`      | With `restate` only: a Restate Cloud API key with the Management API role. Pass it as `TF_VAR_restate_api_key`, so it is not written to a file. The variable is ephemeral, so Terraform keeps the key out of the state and saved plans.                                                                                          |
| `restate_access_token` | With `restate` only, instead of `restate_api_key`: your access token from `restate cloud login`. The module creates the Management API key itself and deletes it with the region. Pass it as `TF_VAR_restate_access_token`, fresh on every run.                                                                                  |
| `tenant_id`            | The Entra tenant of the subscription. The default is the tenant of the credentials Terraform runs with.                                                                                                                                                                                                     |
| `region_grant`         | Creates a region grant instead of a grant for one region, through the `restate` provider: optionally `owner`, `display_name` and `regions`.                                                                                                                                                                 |
| `resource_group`       | The name of the region's resource group. The default is `restate-byoc-region`.                                                                                                                                                                                                                              |

When you apply the grant by hand, leave `restate` unset and pass these from the `grantInputs` that Restate Cloud returned. A new region answers with the first set, and an older region with the second.

| Variable             | From `grantInputs`  | Region                                                         |
| -------------------- | ------------------- | -------------------------------------------------------------- |
| `restate_issuer`     | `restateIssuer`     | A region with its own identity.                                |
| `region_subject`     | `regionSubject`     | A region with its own identity.                                |
| `token_audience`     | `tokenAudience`     | A region with its own identity. The default is the same value. |
| `region_tag`         | `regionTag`         | An older region.                                               |
| `composition_issuer` | `compositionIssuer` | An older region.                                               |

## Outputs

| Output            | Description                                                                                                                                                 |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `grant_id`        | The region grant Restate Cloud created, for `restate_cloud_byoc_region` or `CreateRegion`, when `region_grant` is set.                                      |
| `resource_prefix` | The prefix of every resource name a region of the grant has, when `region_grant` is set.                                                                    |
| `status`          | The grant's status, which is `READY` once it is confirmed, when `region_grant` is set.                                                                      |
| `region_ids`      | The ids of the regions in `region_grant.regions`, by name, when `region_grant` is set.                                                                      |
| `region_id`       | The region Restate Cloud created, when `restate` is set.                                                                                                    |
| `restate_api_key` | The Management API key the module created, when `restate_access_token` is set. It is sensitive.                                                             |
| `grant`           | The tenant, the application's client id and, outside `region_grant` mode, the resource group. When you apply by hand, give this to Restate Cloud unchanged. |
| `subscription_id` | The subscription the region is built in.                                                                                                                    |
