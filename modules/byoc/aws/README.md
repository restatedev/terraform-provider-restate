# AWS

This module creates an IAM role that Restate Cloud assumes to build the region in your AWS account.

## What it creates

| Resource                                           | Purpose                                                                                                                                                                                          |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| The IAM role `restate-cloud-byoc`                  | Restate Cloud assumes this role to build the region. Its trust policy accepts only the region's own identity, or, for an older region, only Restate Cloud's roles with the region's external id. |
| The IAM policy `restate-cloud-byoc-boundary`       | Every role that the region creates must carry this permissions boundary, which limits what those roles can do.                                                                                   |
| An IAM OIDC identity provider for Restate's issuer | AWS checks the region's tokens against this issuer's published keys. The module creates it for a new region, unless you pass `oidc_provider_arn`. An older region has none.                      |

## How Restate Cloud signs in

Restate decides how a region signs in when you create the region. A new region signs in with its own identity, and a region created before regions had their own identity keeps signing in through Restate's roles. With `restate`, the module follows what Restate Cloud answered for the region. In both cases AWS issues short-lived credentials, and no access key exists.

### With the region's own identity

Restate's OIDC issuer signs a short-lived token for the region, and Restate assumes `restate-cloud-byoc` with `sts:AssumeRoleWithWebIdentity`. AWS checks the token against the issuer's published keys through the account's OIDC provider for that issuer.

The role's trust policy accepts the token only when all of these hold:

| Check    | Accepted value                                                                                                      |
| -------- | ------------------------------------------------------------------------------------------------------------------- |
| Issuer   | Restate's issuer, `restate_issuer`, through its OIDC provider. With `restate`, the module takes it from the region. |
| Audience | `sts.amazonaws.com`.                                                                                                |
| Subject  | Exactly the region's subject, `region:<region-id>`. With `restate`, the module takes it from the region.            |

AWS allows one OIDC provider for each issuer in an account. When the account holds more than one region, create the provider once and pass its ARN to every grant as `oidc_provider_arn`, so that no grant owns a provider the others rely on. The grant's own policy can manage only the OIDC providers of the region's EKS clusters, so it cannot change the provider for Restate's issuer.

### Through Restate's roles

Restate Cloud runs in its own AWS account, where it has IAM roles of its own. Those roles assume `restate-cloud-byoc` with `sts:AssumeRole`.

The role's trust policy accepts an assume only when both of these hold:

| Check       | Accepted value                                                                                       |
| ----------- | ---------------------------------------------------------------------------------------------------- |
| Principal   | One of the role ARNs in `restate_principals`. With `restate`, the module takes them from the region. |
| External id | The region's external id. With `restate`, the module takes it from the region.                       |

The trust policy also allows `sts:TagSession` from the same roles. That permission grants nothing on its own, since the assume still needs the external id.

## What Restate Cloud can do with the role

| Area                 | Permissions                                                                                                                                         |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Network and cluster  | Every EC2, EKS and Elastic Load Balancing action, on any resource in the account.                                                                   |
| Snapshot storage     | Every S3 action, on the region's snapshot bucket only. The bucket is named after the region and the account.                                        |
| Roles for the region | Create and change roles, policies and instance profiles whose names start with the region's name. A new role must carry the permissions boundary.   |
| Cluster identity     | Manage the OIDC providers of EKS clusters in the region's AWS region, and create the service-linked roles that EKS and Elastic Load Balancing need. |
| Reading IAM          | Read any role or policy in the account.                                                                                                             |

The role can never change itself, its own policy or the permissions boundary, and it cannot remove the boundary from a role. Because of this, no role the region creates can do more than the boundary allows, and the grant cannot widen its own access.

## Region grant mode

A region grant belongs to a Restate account or organization and serves every region built in this AWS account. You apply the module once for the account. It builds the regions you list, and administrators of the owning account or organization can build more with the grant's id, which needs no change in this account.

Use it when you build many regions in one AWS account, for example one for each of your own customers. A region that stands alone keeps using `restate`, or the per-region inputs.

The module in this mode creates the grant at Restate, creates the role and the boundary for it, confirms the grant and builds the listed regions. It makes its Restate calls through the `restatedev/restate` provider, with the caller's `provider "restate"` configuration.

### Inputs

Set `region_grant` instead of `restate` and the per-region inputs. Terraform refuses to plan a combination with `restate` or a stray per-region input.

| Field          | Description                                                                                                            |
| -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `owner`        | `ACCOUNT`, the default, or `ORGANIZATION`. The administrators of the owner manage the grant and build regions with it. |
| `display_name` | Optional. The default is `aws <AWS account id>`. Changing it later creates a new grant.                                |
| `regions`      | Optional. The regions to build with the grant, by name, each with an optional `cloud_region` (the default is the AWS region the module runs in), `display_name` and `deletion_protection`, which is true by default and must be set to false before the region can be deleted. |

The module also needs `oidc_provider_arn`, the account's OIDC provider for Restate's issuer, which a region grant never creates. It takes no `restate_api_key` or `restate_access_token` in this mode. The `restate` provider's credential decides who the grant belongs to: an account's Management API key or an administrator's token for owner `ACCOUNT`, and an organization administrator's token or an `OrganizationRegionGrantAccess` key of the organization for owner `ORGANIZATION`, with `account_id` set to any account of the organization.

### Example

```hcl
provider "restate" {} # RESTATE_CLOUD_ACCOUNT_ID and RESTATE_CLOUD_MANAGEMENT_API_KEY

module "restate_byoc" {
  source = "github.com/restatedev/terraform-provider-restate//modules/byoc/aws"

  region_grant = {
    owner   = "ORGANIZATION"
    regions = { prod-eu = { deletion_protection = true } }
  }
  oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud"
}
```

The module outputs `grant_id`, `resource_prefix`, `status`, `region_ids` and `grant`. Elsewhere, administrators pass `grant_id` to `restate_cloud_byoc_region` or as `grantId` to `CreateRegion`.

### What the module does in one apply

1. `restate_cloud_region_grant` calls `CreateRegionGrant` with the owner and this AWS account. Restate answers with the grant's id, Restate's issuer, the subject `grant:<grant id>`, the audience and the resource prefix `rgr-<grant>`.
2. It creates the role `restate-cloud-<resource prefix>` and the boundary `restate-cloud-<resource prefix>-boundary`, named after the grant so that they cannot collide with a region's role in the same account. The role's trust policy accepts the account's OIDC provider with the grant's subject and the audience, and its permissions apply to names that start with the resource prefix.
3. `restate_cloud_region_grant_confirmation` calls `ConfirmRegionGrant` with the role and the boundary. Restate signs in through the role and checks that it acts in this AWS account. AWS documents no figure for how long an IAM change takes to be visible everywhere, so Restate keeps signing in in the background for up to 20 minutes, and the apply waits until the grant is `READY`, for five minutes by default.
4. Each listed region is built with the grant, and the apply waits until it is `ACTIVE`.

If the grant is not `READY` by then, the apply fails with the cloud's last answer and the grant stays `PENDING`. Run `terraform apply` again, which keeps waiting while Restate is still signing in, and starts a new sign-in once Restate has given up and you have fixed the cause. A grant that stays unconfirmed for seven days expires.

### A grant that ended

A grant can end outside Terraform, for example when an administrator deletes it or when it expires. A refresh then notices it, and the next apply creates a new grant, updates the role for it and confirms it.

### What a destroy does

The listed regions are deleted first, and the apply waits until Restate has removed what it built. The confirmation's destroy then deletes the grant at Restate, before the role and the boundary go. Restate answers 409 while any region still uses the grant, including regions that other configurations built with it, which stops the destroy and keeps the role.

### What a region grant reaches

The permissions are those of a region's own grant, widened from one region to every region of the grant. Every name the grant covers starts with its resource prefix `rgr-<grant>-`.

| Area                  | Permissions                                                                                                                                                                            |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Network and cluster   | Every EC2, EKS and Elastic Load Balancing action, on any resource in the account.                                                                                                      |
| Snapshot storage      | Every S3 action, on buckets named `rgr-<grant>-*-snapshots-<account id>`, which is the pattern of the buckets the composition creates.                                                 |
| Roles for the regions | Create and change roles, policies and instance profiles whose names start with the prefix. A new role must carry the permissions boundary, and `iam:PassRole` covers only these roles. |
| EKS OIDC providers    | Create, change and delete the IAM OIDC providers of EKS clusters in any AWS region. The role can change or delete only providers that carry the grant's tag.                           |

The role can never change itself, its policy or the boundary, exactly as for a region's own grant. Regions of one grant share the prefix and the identity, so they can reach each other's resources. Use separate AWS accounts when two regions need a hard boundary.

### Changing a grant

A grant keeps the settings it was created with. A change to `owner`, `display_name` or the AWS account replaces the grant and its confirmation together, and Restate refuses the replacement while a region uses the grant. When regions exist, delete them before you change these inputs.

### EKS OIDC providers

Each EKS cluster has one IAM OIDC provider, and a region grant serves clusters in more than one AWS region. The same account may run your own EKS clusters, so the composition tags every provider it creates with `restate.cloud/region-grant=<grant id>`. The role may use them on these terms.

| Action                                                                                | Condition                                                                                            |
| ------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Create a provider, which AWS also authorizes as `iam:TagOpenIDConnectProvider`        | The create call carries the tag `restate.cloud/region-grant` with the grant's id (`aws:RequestTag`). |
| Delete a provider, add or remove a client id or a thumbprint, tag or untag other keys | The provider already carries the tag with the grant's id (`aws:ResourceTag`).                        |
| Tag a provider whose grant tag holds another grant's id                               | Denied.                                                                                              |
| Set the grant's tag to any id but the grant's own, or remove it                       | Denied.                                                                                              |

The condition on a create does not limit the tag keys to a list, because the Crossplane AWS provider adds its own `crossplane-kind`, `crossplane-name` and `crossplane-providerconfig` tags to the call.

IAM has a limit here that this module cannot remove. Creating a provider with tags requires `iam:TagOpenIDConnectProvider` next to `iam:CreateOpenIDConnectProvider`, and the condition keys of that tag check are the same for a create and for a call on an existing provider. No key tells the two apart. The role can therefore tag an existing EKS OIDC provider that carries no grant tag, such as one of your own clusters, with the grant's id, and then change or delete it. Restate has accepted this limit for region grants. The denies in the table stop a tag that is already set from being rewritten or removed, so a provider that belongs to one grant stays with that grant. They compare the tag's value with the grant's id, because IAM reads the tags of a create call as the provider's tags while it authorizes that call. A deny on the tag's presence alone would therefore refuse every create.

A region-scoped grant already allows every OIDC provider action on every EKS provider of its AWS region, with no tag check at all. A region grant adds providers in the other AWS regions, and it reaches a provider that carries no grant tag only through the tagging step described above.

### Destroy

`terraform destroy` calls `DeleteRegionGrant` before it removes anything in the account. Restate refuses with 409 while a region uses the grant, which stops the destroy and keeps the role, the policy and the boundary. Delete the regions first, then destroy again. The role and the boundary are removed only after Restate has deleted the grant.

Revoking a grant in AWS by other means leaves its regions running, but Restate can no longer change or repair them until the grant is restored.

### The OIDC provider

AWS allows one OIDC provider for each issuer in an account, and every grant in the account trusts it. Destroying the state that created it cuts off every grant, so a region grant never creates or deletes it. The `oidc-provider/` directory is a small root module with a state of its own that creates it.

```bash
cd oidc-provider
terraform init
terraform apply -var restate_issuer=https://oidc.restate.cloud   # the restateIssuer of the grant's inputs
terraform output arn
```

Pass the ARN as `oidc_provider_arn`. The module refuses an ARN that does not belong to the grant's issuer.

The provider resource has `prevent_destroy` set, so a `terraform destroy` in that directory fails and no grant loses its identity by accident. To remove the provider on purpose, delete the `lifecycle` block from `oidc-provider/main.tf`, run `terraform apply`, and then run `terraform destroy`. Do this only after every grant in the account has been destroyed.

An account may already hold the provider, because a region-scoped grant created it. Move it into the new state before the account's first region grant.

1. In `oidc-provider/`, import the existing provider into the new state.

   ```hcl
   import {
     to = aws_iam_openid_connect_provider.restate
     id = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud"
   }
   ```

   Run `terraform plan` to check that it shows an import and no change, then `terraform apply`.

2. In the region-scoped grant's own configuration, pass the provider's ARN and release the resource from that state in the same apply.

   ```hcl
   oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud"

   removed {
     from = aws_iam_openid_connect_provider.restate
     lifecycle {
       destroy = false
     }
   }
   ```

   Without the `removed` block the plan would destroy the provider. If your Terraform predates `removed` blocks, run `terraform state rm 'aws_iam_openid_connect_provider.restate[0]'` before the apply. The plan must show no change to the role.

## Inputs

| Variable               | Description                                                                                                                                                                                                                                                                                 |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `restate`              | Your Restate Cloud account and the region: `account_id`, `name`, and optionally `display_name`, `api_url` and `region_identity`. Set `region_identity = false` for a new region while Restate Cloud gives no region its own identity in production, and leave it unset for an existing one. |
| `region_grant`         | Instead of `restate`: creates a region grant that serves every region built in this account.                                                                                                                                                                                                |
| `restate_api_key`      | A Restate Cloud API key with the Management API role. Pass it as `TF_VAR_restate_api_key`, so it is not written to a file. The variable is ephemeral, so Terraform keeps the key out of the state and saved plans.                                                                          |
| `restate_access_token` | Instead of `restate_api_key`: your access token from `restate cloud login`. The module creates the Management API key itself and deletes it with the region. Pass it as `TF_VAR_restate_access_token`, fresh on every run.                                                                  |
| `role_name`            | The name of the role, and of the boundary with `-boundary` added. The default is `restate-cloud-byoc`, or `restate-cloud-<resource prefix>` for a region grant, so that it cannot collide with a region's role in the same account. It must not start with the region's name or the grant's resource prefix.                                                                                                                                            |
| `oidc_provider_arn`    | An OIDC provider for Restate's issuer that already exists in the account. Unset, a grant for a region with its own identity creates the provider itself. Required with `region_grant`.                                                                                                      |

When you apply the grant by hand, leave `restate` unset and pass these from the `grantInputs` that Restate Cloud returned. A new region answers with the first set, and an older region with the second.

| Variable             | From `grantInputs`  | Region                                                         |
| -------------------- | ------------------- | -------------------------------------------------------------- |
| `region_name`        | `regionName`        | Every region.                                                  |
| `restate_issuer`     | `restateIssuer`     | A region with its own identity.                                |
| `region_subject`     | `regionSubject`     | A region with its own identity.                                |
| `token_audience`     | `tokenAudience`     | A region with its own identity. The default is the same value. |
| `external_id`        | `externalId`        | An older region.                                               |
| `restate_principals` | `restatePrincipals` | An older region.                                               |

## Outputs

| Output              | Description                                                                                                                                 |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `region_id`         | The region Restate Cloud created, when `restate` is set.                                                                                    |
| `restate_api_key`   | The Management API key the module created, when `restate_access_token` is set. It is sensitive.                                             |
| `grant`             | The role and the boundary. When you apply by hand, give this to `SetRegionGrant` unchanged.                                                 |
| `grant_id`          | The region grant Restate Cloud created, when `region_grant` is set.                                                                         |
| `resource_prefix`   | The prefix of every resource name a region of the grant has, when `region_grant` is set.                                                    |
| `status`            | The grant's status as Restate Cloud last answered, when `region_grant` is set. It is `READY` once confirmed.                                |
| `external_id`       | The external id the role's trust policy requires, for an older region. It is null for a region with its own identity.                       |
| `oidc_provider_arn` | The OIDC provider for Restate's issuer, which another grant in the account can pass as `oidc_provider_arn`. It is null for an older region. |
| `account_id`        | The AWS account the region is built in.                                                                                                     |
