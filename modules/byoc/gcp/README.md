# Google Cloud

This module creates a service account that Restate Cloud uses to build the region in your Google Cloud project.

## What it creates

| Resource                                                     | Purpose                                                                                                                                                                                                                                                |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| The service account `restate-cloud-byoc`                     | Restate Cloud builds the region as this account. In a separate identity project it is named `restate-<region>`.                                                                                                                                        |
| The workload identity pool `restate-cloud`, and its provider | In a separate identity project it is named `restate-<region>`. The pool accepts sign-ins from the region's own identity, or, for an older region, from Restate Cloud's own identities, and from no one else, and lets them act as the service account. |
| Enabled APIs                                                 | Compute Engine, Kubernetes Engine, Cloud Storage, IAM and Resource Manager, which the region uses. They stay enabled after a destroy.                                                                                                                  |

## How Restate Cloud signs in

Restate Cloud signs in through OpenID Connect (OIDC) federation. It presents a short-lived token, signed by its OIDC issuer, and Google Cloud checks the token against the issuer's published keys, then exchanges it for a short-lived credential of the service account `restate-cloud-byoc`. No service account key exists.

Restate decides which token a region presents when you create the region. With `restate`, the module follows what Restate Cloud answered for the region, and a new region needs no setting for it.

### With the region's own identity

A new region presents a token from Restate's issuer whose subject names the region. The pool's provider, `restate-region`, accepts a token only when all of these hold, and only that subject may act as the service account:

| Check    | Accepted value                                                                                           |
| -------- | -------------------------------------------------------------------------------------------------------- |
| Issuer   | Restate's issuer, `restate_issuer`. With `restate`, the module takes it from the region.                 |
| Audience | `restate-cloud-region`.                                                                                  |
| Subject  | Exactly the region's subject, `region:<region-id>`. With `restate`, the module takes it from the region. |

The project needs no label.

### Through the composition cluster

A region created before regions had their own identity presents a token that names one of Restate's identities as its subject. The pool's provider, `composition-cluster`, accepts a token only when all of these hold:

| Check    | Accepted value                                                                                                    |
| -------- | ----------------------------------------------------------------------------------------------------------------- |
| Issuer   | Restate Cloud's issuer, `composition_issuer`. With `restate`, the module takes it from the region.                |
| Audience | `restate-cloud-composition`.                                                                                      |
| Subject  | One of the exact identities in `provider_service_accounts`, plus one for the region. Prefixes are never accepted. |

## What Restate Cloud can do with the service account

| Area                | Permissions                                                                                                                |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Network and cluster | Compute Admin and Kubernetes Engine Admin on the project.                                                                  |
| Snapshot storage    | Storage Admin, on the region's snapshot bucket only. The bucket is named after the region and the project.                 |
| Service accounts    | Service Account Admin and Service Account User on the project, so it can create the accounts the region's cluster runs as. |
| Project IAM         | Grant or revoke the role Kubernetes Engine nodes need, and no other role.                                                  |

By default the service account and its pool live in the region's project. Service Account Admin on that project then also covers the service account itself, so it could change its own access. To prevent this, set `identity_project` to a second project, which then holds the service account and the pool.

Several regions can share one identity project. There the service account and the pool are named after the region, `restate-<region>`, so that each region has its own and destroying one region leaves the others' identity in place. A region name longer than 22 characters is shortened and ends in a short hash, because a service account ID is at most 30 characters.

A region that set `identity_project` with an earlier version of this module has the names `restate-cloud` and `restate-cloud-byoc`. Set `pool_id = "restate-cloud"` and `service_account_id = "restate-cloud-byoc"` before you upgrade. Otherwise the module deletes the region's pool and service account and creates new ones under the new names, which cuts Restate Cloud off from the region while it happens.

## If the state is lost

Google Cloud keeps the pool, the service account and their bindings when the module's state is lost, so the next apply fails with `Error 409` and `already exists`. Import these objects into the new state instead of deleting them, because Google Cloud reserves a deleted pool's ID for 30 days. With `restate`, the next apply also reserves a new region in Restate Cloud, and the old region stays there until you delete it.

Add these import blocks to the root module, and replace the module name, the project, the project number and the region with yours. Remove the blocks after the apply.

```hcl
locals {
  project = "my-project"
  account = "restate-cloud-byoc@my-project.iam.gserviceaccount.com"
  pool    = "projects/my-project/locations/global/workloadIdentityPools/restate-cloud"
}

import {
  to = module.grant.google_iam_workload_identity_pool.restate
  id = local.pool
}

import {
  to = module.grant.google_iam_workload_identity_pool_provider.composition
  id = "${local.pool}/providers/restate-region"
}

import {
  to = module.grant.google_service_account.grant
  id = "projects/${local.project}/serviceAccounts/${local.account}"
}

import {
  to = module.grant.google_service_account_iam_member.federation
  id = "projects/${local.project}/serviceAccounts/${local.account} roles/iam.workloadIdentityUser principal://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/restate-cloud/subject/region:rgn_example"
}

import {
  for_each = toset(["roles/container.admin", "roles/compute.admin", "roles/iam.serviceAccountAdmin", "roles/iam.serviceAccountUser"])
  to       = module.grant.google_project_iam_member.grant[each.value]
  id       = "${local.project} ${each.value} serviceAccount:${local.account}"
}

import {
  to = module.grant.google_project_iam_member.grant_storage
  id = "${local.project} roles/storage.admin serviceAccount:${local.account} Region snapshot buckets only"
}

import {
  to = module.grant.google_project_iam_member.grant_iam
  id = "${local.project} roles/resourcemanager.projectIamAdmin serviceAccount:${local.account} Region roles only"
}
```

An older region differs in two IDs. Its pool's provider is `composition-cluster`, and its federation member is `principalSet://iam.googleapis.com/projects/<project number>/locations/global/workloadIdentityPools/<pool>/*`. `gcloud iam service-accounts get-iam-policy` on the service account shows the exact member. The two conditional bindings end in the title of their condition, which is part of their ID. With `identity_project`, the pool, the service account and the federation binding are in the identity project, and the other bindings stay on the region's project.

The plan should then show only imports. When the apply reserves a new region, the plan also points the pool's provider, and for a region with its own identity the federation binding, at the new region.

## Region grant mode

A region grant belongs to your Restate account or organization and lets Restate Cloud build any number of regions in one project. You apply the module once for the project, and it builds the regions you list in `region_grant.regions`. Administrators of the owning account or organization can also build regions with the grant's id elsewhere, with `restate_cloud_byoc_region` or the Cloud API, and nothing in your project changes for each new region.

Use it when you build many regions in one project, for example one for each of your own customers. A grant for a single region (the `restate` input) remains the better fit when you want Google Cloud to enforce that each region has its own identity, because the regions of one grant share the service account and the bucket prefix and can reach each other's resources.

Your Restate account needs BYOC authorization before it can create a region grant. Ask Restate to enable it.

### Inputs

Set `region_grant` instead of `restate`. The module accepts exactly one of `restate`, `region_grant` or the region's identifiers passed by hand, and Terraform refuses to plan a combination.

| Field          | Description                                                                                                         |
| -------------- | ------------------------------------------------------------------------------------------------------------------- |
| `owner`        | `ACCOUNT` (the default) or `ORGANIZATION`. An organization grant builds regions in any account of the organization. |
| `display_name` | Optional. The grant's name in Restate Cloud. Restate Cloud chooses one when it is unset.                            |
| `regions`      | Optional. The regions to build with the grant, by name. Each needs a `cloud_region`, for example `europe-west1`, because the module has no location of its own in this mode, and takes an optional `display_name`, `machine_type` and `deletion_protection`, which is true by default and must be set to false before the region can be deleted. |

The grant covers the project in `project`, which must be dedicated to the regions of the grant. Leave `location`, `region_name`, `region_tag` and the other per-region inputs unset.

In this mode the module makes its Restate Cloud calls through the `restatedev/restate` provider, with your `provider "restate"` configuration, so it takes no `restate_api_key` or `restate_access_token`. The provider's credential decides who the grant belongs to. For `owner = "ACCOUNT"`, use an account key with the Management API role or an account administrator's token. For `ORGANIZATION`, use an organization administrator's token or an organization key with the `OrganizationRegionGrantAccess` role, and set the provider's `account_id` to any account of the organization.

```hcl
provider "restate" {} # RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY

module "restate_byoc" {
  source = "github.com/restatedev/terraform-provider-restate//modules/byoc/gcp"

  project = "acme-restate-prod"
  region_grant = {
    owner        = "ORGANIZATION"
    display_name = "Acme production project"
    regions      = { prod-eu = { cloud_region = "europe-west1" } }
  }
}
```

The module outputs `grant_id`, `resource_prefix`, `status`, `region_ids` and `grant`. Elsewhere, administrators pass `grant_id` to `restate_cloud_byoc_region`, or as `grantId` to `CreateRegion`.

### What it creates

The service account, the pool and the roles are the ones described above, with these differences.

| Area            | Region grant                                                                                                                                                                                     |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Pool provider   | `restate-grant`. It accepts a token only from Restate's issuer, with the audience Restate Cloud gives for the grant, and only for the subject `grant:<grant id>`.                                |
| Impersonation   | Only the subject `grant:<grant id>` may act as the service account.                                                                                                                              |
| Snapshot bucket | Storage Admin reaches only buckets whose names start with the grant's resource prefix, `rgr-<grant>-`, and end with `-snapshots-<project>`. Every region of the grant names its bucket that way. |
| Project label   | None. The module neither labels the project nor creates a region.                                                                                                                                |
| Identity names  | `restate-<resource prefix>` for both the pool and the service account, in the region project or in a separate `identity_project`, which several grants can then share. Google Cloud keeps a deleted pool's ID for 30 days, and names that carry the grant's id let a grant be made again after a destroy. |

With `identity_project`, the module also enables Cloud Resource Manager in that project, because Restate Cloud reads the project's permissions through the service account when it checks the grant.

### Confirmation

After the module has created the grant in your project, `restate_cloud_region_grant_confirmation` calls `ConfirmRegionGrant`. Restate Cloud signs in through the grant and tests that the service account holds the project permissions the regions need, which proves that the project is the one you named. The grant becomes `READY` and the `status` output shows it. The listed regions are then built with the grant, and the apply waits until each one is `ACTIVE`.

Google Cloud documents that an IAM change usually takes effect within two minutes and can take seven minutes or longer. While the change spreads, Restate Cloud keeps signing in in the background, and the apply waits up to 10 minutes for the grant to become `READY`. If it is not `READY` by then, the grant stays `PENDING` and the apply fails with the cloud's last answer. Run `terraform apply` again, which keeps waiting without creating anything new. A grant that stays `PENDING` for seven days expires.

A project can have one confirmed region grant at a time.

### A grant that ended

A grant can end outside Terraform, for example when an administrator deletes it or when it expires. A refresh then notices it, and the next apply creates a new grant, updates the pool and the service account for it and confirms it.

### Destroy

`terraform destroy` deletes the listed regions first and waits until Restate Cloud has removed what it built. It then asks Restate Cloud to delete the grant before it removes anything from your project. Restate Cloud answers with a conflict while any region still uses the grant, including regions that other configurations built with it. The destroy then stops and keeps the service account and the pool, which Restate Cloud needs to delete those regions. Delete the regions first, then run the destroy again. Your project's identity is removed only after Restate Cloud has answered.

### Changing a grant

A grant keeps the settings it was created with. A change to `owner`, `display_name` or `project` replaces the grant and its confirmation together, and the replacement is refused while a region uses the grant. When regions exist, delete them before you change these inputs.

If the state is lost, applying again with the same settings returns the same grant. Import the project's objects as in the section on lost state, using the names this mode gives them.

## Inputs

| Variable               | Description                                                                                                                                                                                                                                                                                  |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `project`              | The project the region is built in.                                                                                                                                                                                                                                                          |
| `location`             | The region's location, for example `europe-west1`. Needed with `restate`.                                                                                                                                                                                                                    |
| `region_grant`         | Creates a region grant instead of a grant for one region, through the `restate` provider: optionally `owner`, `display_name` and `regions`.                                                                                                                                                  |
| `restate`              | Your Restate Cloud account and the region: `account_id`, `name`, and optionally `display_name`, `machine_type`, `api_url` and `region_identity`. Set `region_identity = true` to ask for a region that signs in with its own identity, which Restate Cloud does not offer in production yet. |
| `restate_api_key`      | With `restate` only: a Restate Cloud API key with the Management API role. Pass it as `TF_VAR_restate_api_key`, so it is not written to a file. The variable is ephemeral, so Terraform keeps the key out of the state and saved plans.                                                                           |
| `restate_access_token` | With `restate` only, instead of `restate_api_key`: your access token from `restate cloud login`. The module creates the Management API key itself and deletes it with the region. Pass it as `TF_VAR_restate_access_token`, fresh on every run.                                                                   |
| `identity_project`     | A second project for the service account and its pool. The default is the region's project.                                                                                                                                                                                                  |
| `pool_id`              | The workload identity pool's ID. The default is `restate-cloud`, `restate-<region>` in a separate identity project, or `restate-<resource prefix>` for a region grant.                                                                                                                                                                       |
| `service_account_id`   | The service account's ID. The default is `restate-cloud-byoc`, `restate-<region>` in a separate identity project, or `restate-<resource prefix>` for a region grant.                                                                                                                                                                         |

When you apply the grant by hand, leave `restate` unset and pass these from the `grantInputs` that Restate Cloud returned. A new region answers with the first set, and an older region with the second.

| Variable             | From `grantInputs`  | Region                                                         |
| -------------------- | ------------------- | -------------------------------------------------------------- |
| `restate_issuer`     | `restateIssuer`     | A region with its own identity.                                |
| `region_subject`     | `regionSubject`     | A region with its own identity.                                |
| `token_audience`     | `tokenAudience`     | A region with its own identity. The default is the same value. |
| `region_name`        | `regionName`        | An older region.                                               |
| `composition_issuer` | `compositionIssuer` | An older region.                                               |
| `region_tag`         | `regionTag`         | An older region.                                               |

For an older region, the module labels the project `restate-cloud-region-<region tag>` with the region tag. For now it also sets `restate-cloud-region` to the same value, for Restate releases that read only that key. It keeps the project's other labels, and on destroy removes only those two labels. The module writes the project's labels as it read them when planning, so do not change them while an apply runs.

## Outputs

| Output            | Description                                                                                                                        |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `region_id`       | The region Restate Cloud created, when `restate` is set.                                                                           |
| `restate_api_key` | The Management API key the module created, when `restate_access_token` is set. It is sensitive.                                    |
| `grant`           | The identity provider, the service account and the identity project. When you apply by hand, give this to Restate Cloud unchanged. |
| `grant_id`        | With `region_grant`: the grant's id, for `restate_cloud_byoc_region` or `CreateRegion`.                                            |
| `resource_prefix` | With `region_grant`: the prefix of every resource name a region of the grant has in the project.                                   |
| `status`          | With `region_grant`: the grant's status, `READY` once Restate Cloud has confirmed it.                                              |
| `region_ids`      | With `region_grant`: the ids of the regions in `region_grant.regions`, by name.                                                    |
