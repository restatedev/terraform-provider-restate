mock_provider "aws" {
  override_data {
    target = data.aws_servicequotas_service_quota.elastic_ips
    values = { value = 6 }
  }
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012" }
  }
  override_data {
    target = data.aws_partition.current
    values = { partition = "aws" }
  }
  override_data {
    target = data.aws_region.current
    values = { region = "eu-central-1" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/restate-cloud-rgr-x5z2y4qjmt" }
  }
  mock_resource "aws_iam_policy" {
    defaults = { arn = "arn:aws:iam::123456789012:policy/restate-cloud-rgr-x5z2y4qjmt-boundary" }
  }
  mock_resource "aws_iam_openid_connect_provider" {
    defaults = { arn = "arn:aws:iam::123456789012:oidc-provider/oidc.dev.restate.cloud" }
  }
}

mock_provider "restapi" {}

# What Restate Cloud answers for a new grant, as the restate provider exports it.
mock_provider "restate" {
  mock_resource "restate_cloud_region_grant" {
    defaults = {
      id              = "rgr_x5z2y4qjmt"
      owner_id        = "acc_1a2b3c4d5e"
      status          = "PENDING"
      restate_issuer  = "https://oidc.dev.restate.cloud"
      grant_subject   = "grant:rgr_x5z2y4qjmt"
      token_audience  = "sts.amazonaws.com"
      resource_prefix = "rgr-x5z2y4qjmt"
    }
  }
  mock_resource "restate_cloud_region_grant_confirmation" {
    defaults = { id = "rgr_x5z2y4qjmt", status = "READY" }
  }
  mock_resource "restate_cloud_byoc_region" {
    defaults = { id = "rgn_a1b2c3d4e5", status = "ACTIVE" }
  }
}

variables {
  oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/oidc.dev.restate.cloud"
  # terraform test fails an apply run whose ephemeral variable is unset, so the key is set here
  # even for the runs that do not use it.
  restate_api_key = "key_unused"
}

run "account_grant" {
  variables {
    region_grant = {
      display_name = "Replit AWS"
      regions      = { prod-eu = {}, prod-us = { cloud_region = "us-east-1", deletion_protection = false } }
    }
  }

  assert {
    condition = (
      restate_cloud_region_grant.this[0].owner == "ACCOUNT" && restate_cloud_region_grant.this[0].display_name == "Replit AWS" &&
      restate_cloud_region_grant.this[0].cloud == "aws" && restate_cloud_region_grant.this[0].cloud_account_id == "123456789012"
    )
    error_message = "The grant must name the owner, the cloud and this AWS account."
  }

  assert {
    condition     = aws_iam_role.grant.name == "restate-cloud-rgr-x5z2y4qjmt" && aws_iam_policy.boundary.name == "restate-cloud-rgr-x5z2y4qjmt-boundary"
    error_message = "A grant's role and boundary carry its id, so that they cannot collide with a region's restate-cloud-byoc role in the same account."
  }

  # The trust policy
  assert {
    condition = jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Principal.Federated == var.oidc_provider_arn && jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Action == ["sts:AssumeRoleWithWebIdentity"] && jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Condition.StringEquals == {
      "oidc.dev.restate.cloud:sub" = "grant:rgr_x5z2y4qjmt"
      "oidc.dev.restate.cloud:aud" = "sts.amazonaws.com"
    }
    error_message = "A region grant must trust the passed provider with the grant's subject and the audience."
  }
  assert {
    condition     = length(aws_iam_openid_connect_provider.restate) == 0 && output.oidc_provider_arn == var.oidc_provider_arn
    error_message = "A region grant must never create the issuer's OIDC provider."
  }

  # Names are scoped by the grant's prefix
  assert {
    condition     = [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "BoundedRoles"][0] == "arn:aws:iam::123456789012:role/rgr-x5z2y4qjmt-*" && [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "RegionPolicies"][0] == "arn:aws:iam::123456789012:policy/rgr-x5z2y4qjmt-*" && [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "InstanceProfiles"][0] == "arn:aws:iam::123456789012:instance-profile/rgr-x5z2y4qjmt-*" && [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "PassRegionRoles"][0] == "arn:aws:iam::123456789012:role/rgr-x5z2y4qjmt-*"
    error_message = "IAM names must start with the grant's resource prefix."
  }
  assert {
    condition     = jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "SnapshotBucket"][0]) == jsonencode(["arn:aws:s3:::rgr-x5z2y4qjmt-*-snapshots-123456789012", "arn:aws:s3:::rgr-x5z2y4qjmt-*-snapshots-123456789012/*"]) && jsonencode([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s.Resource if s.Sid == "SnapshotBucket"][0]) == jsonencode(["arn:aws:s3:::rgr-x5z2y4qjmt-*-snapshots-123456789012", "arn:aws:s3:::rgr-x5z2y4qjmt-*-snapshots-123456789012/*"])
    error_message = "The snapshot bucket pattern must match rgr-<grant>-<region>-snapshots-<account>, which the composition creates."
  }
  assert {
    condition     = [for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s.Resource if s.Sid == "PassRegionRoles"][0] == "arn:aws:iam::123456789012:role/rgr-x5z2y4qjmt-*"
    error_message = "The boundary's PassRole must use the grant's prefix."
  }

  # EKS OIDC providers: any AWS region, create only with the grant's tag, change only tagged ones
  assert {
    condition     = [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Sid if endswith(s.Sid, "Issuer") || startswith(s.Sid, "ClusterIssuer") || startswith(s.Sid, "DenyGrantTag")] == ["ClusterIssuerCreate", "ClusterIssuerUpkeep", "DenyGrantTagTakeover", "DenyGrantTagRewrite", "DenyGrantTagRemoval"]
    error_message = "A region grant must carry the five EKS OIDC provider statements and no unconditioned one."
  }
  assert {
    condition = (
      [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "ClusterIssuerCreate"][0].Resource == "arn:aws:iam::123456789012:oidc-provider/oidc.eks.*.amazonaws.com/id/*" &&
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "ClusterIssuerCreate"][0].Action) == jsonencode(["iam:CreateOpenIDConnectProvider", "iam:TagOpenIDConnectProvider"]) &&
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "ClusterIssuerCreate"][0].Condition) == jsonencode({ StringEquals = { "aws:RequestTag/restate.cloud/region-grant" = "rgr_x5z2y4qjmt" } })
    )
    error_message = "A provider may be created only in a call that tags it with the grant's id, and Create needs Tag as its dependent action."
  }
  assert {
    condition = (
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "ClusterIssuerUpkeep"][0].Condition) == jsonencode({ StringEquals = { "aws:ResourceTag/restate.cloud/region-grant" = "rgr_x5z2y4qjmt" } }) &&
      !contains([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "ClusterIssuerUpkeep"][0].Action, "iam:CreateOpenIDConnectProvider")
    )
    error_message = "Change and delete must reach only providers that carry the grant's tag."
  }
  assert {
    condition = (
      [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "DenyGrantTagTakeover"][0].Effect == "Deny" &&
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "DenyGrantTagTakeover"][0].Condition) == jsonencode({ Null = { "aws:ResourceTag/restate.cloud/region-grant" = "false" }, StringNotEquals = { "aws:ResourceTag/restate.cloud/region-grant" = "rgr_x5z2y4qjmt" } }) &&
      [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "DenyGrantTagRewrite"][0].Effect == "Deny" &&
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "DenyGrantTagRewrite"][0].Condition) == jsonencode({ Null = { "aws:RequestTag/restate.cloud/region-grant" = "false" }, StringNotEquals = { "aws:RequestTag/restate.cloud/region-grant" = "rgr_x5z2y4qjmt" } }) &&
      jsonencode([for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s if s.Sid == "DenyGrantTagRemoval"][0].Condition) == jsonencode({ "ForAnyValue:StringEquals" = { "aws:TagKeys" = ["restate.cloud/region-grant"] } })
    )
    error_message = "A provider's grant tag must not be rewritten or removed."
  }
  assert {
    condition     = !strcontains(aws_iam_role_policy.grant.policy, "oidc.eks.eu-central-1")
    error_message = "A region grant reaches EKS providers in every AWS region, not the provider's region."
  }

  # IAM limits a role's inline policy to 10,240 characters, and the boundary to 6,144.
  assert {
    condition     = length(aws_iam_role_policy.grant.policy) < 9000 && length(aws_iam_policy.boundary.policy) < 5000
    error_message = "The grant's policies must stay clear of IAM's size limits."
  }

  # Confirmation and regions
  assert {
    condition     = restate_cloud_region_grant_confirmation.this[0].grant_id == "rgr_x5z2y4qjmt" && restate_cloud_region_grant_confirmation.this[0].grant == tomap({ roleArn = aws_iam_role.grant.arn, permissionsBoundary = aws_iam_policy.boundary.arn })
    error_message = "The confirmation must carry the grant id, the role and the boundary."
  }
  assert {
    condition = (
      restate_cloud_byoc_region.this["prod-eu"].cloud_region == "eu-central-1" && restate_cloud_byoc_region.this["prod-us"].cloud_region == "us-east-1" &&
      restate_cloud_byoc_region.this["prod-eu"].grant_id == "rgr_x5z2y4qjmt" &&
      restate_cloud_byoc_region.this["prod-eu"].deletion_protection && !restate_cloud_byoc_region.this["prod-us"].deletion_protection
    )
    error_message = "Each region must be built with the confirmed grant, in the provider's AWS region unless it names another."
  }

  # Outputs
  assert {
    condition     = output.grant_id == "rgr_x5z2y4qjmt" && output.resource_prefix == "rgr-x5z2y4qjmt" && output.status == "READY" && output.region_ids == { prod-eu = "rgn_a1b2c3d4e5", prod-us = "rgn_a1b2c3d4e5" }
    error_message = "A region grant must output its id, prefix, status and regions."
  }
  assert {
    condition     = output.grant == { roleArn = aws_iam_role.grant.arn, permissionsBoundary = aws_iam_policy.boundary.arn } && output.external_id == null && output.restate_api_key == null && output.region_id == null
    error_message = "The grant output must be what the confirmation takes, and nothing of a region's own mode may be set."
  }
  assert {
    condition     = length(restapi_object.api_key) == 0 && length(restapi_object.reservation) == 0
    error_message = "In grant mode the module must make no restapi call."
  }
}

run "organization_grant" {
  command = plan

  variables {
    region_grant = { owner = "ORGANIZATION" }
  }

  assert {
    condition     = restate_cloud_region_grant.this[0].owner == "ORGANIZATION" && restate_cloud_region_grant.this[0].display_name == "aws 123456789012" && length(restate_cloud_byoc_region.this) == 0
    error_message = "An organization grant must ask for owner ORGANIZATION, with the default display name, and build no region unasked."
  }
}

# The grant's role keeps the shape of a region's own grant in every other mode.
run "region_scoped_grant_is_unchanged" {
  variables {
    region_name        = "prod-eu-x5z2y4qjmt"
    external_id        = "rgn_x5z2y4qjmt"
    restate_principals = ["arn:aws:iam::111111111111:role/restate-composition-provider"]
    oidc_provider_arn  = null
  }

  assert {
    condition     = [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "BoundedRoles"][0] == "arn:aws:iam::123456789012:role/prod-eu-x5z2y4qjmt*" && [for s in jsondecode(aws_iam_role_policy.grant.policy).Statement : s.Resource if s.Sid == "ClusterIssuer"][0] == "arn:aws:iam::123456789012:oidc-provider/oidc.eks.eu-central-1.amazonaws.com/id/*"
    error_message = "A region-scoped grant must keep its names and its EKS provider statement."
  }
  assert {
    condition     = length(restate_cloud_region_grant.this) == 0 && length(restate_cloud_region_grant_confirmation.this) == 0 && output.grant_id == null && output.resource_prefix == null && output.status == null
    error_message = "Grant mode objects must not exist in another mode."
  }
}

run "grant_needs_the_issuers_provider" {
  command = plan

  variables {
    region_grant      = {}
    restate_api_key   = "key_test"
    oidc_provider_arn = null
  }


  expect_failures = [aws_iam_role.grant]
}

run "grant_provider_must_match_the_issuer" {
  command = apply

  variables {
    region_grant      = {}
    restate_api_key   = "key_test"
    oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/oidc.other.example"
  }


  expect_failures = [aws_iam_role.grant]
}

run "grant_excludes_restate" {
  command = plan

  variables {
    region_grant    = {}
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu", region_identity = true }
    restate_api_key = "key_test"
  }

  expect_failures = [var.region_grant]
}

run "grant_excludes_the_per_region_inputs" {
  command = plan

  variables {
    region_grant       = {}
    restate_api_key    = "key_test"
    region_name        = "prod-eu-x5z2y4qjmt"
    external_id        = "rgn_x5z2y4qjmt"
    restate_principals = ["arn:aws:iam::111111111111:role/restate-composition-provider"]
  }

  expect_failures = [var.region_grant]
}

run "grant_excludes_the_region_identity_inputs" {
  command = plan

  variables {
    region_grant    = {}
    restate_api_key = "key_test"
    restate_issuer  = "https://oidc.restate.cloud"
    region_subject  = "region:rgn_x5z2y4qjmt"
  }

  expect_failures = [var.region_grant]
}

run "grant_ignores_a_module_credential" {
  command = plan

  variables {
    region_grant         = {}
    restate_api_key      = null
    restate_access_token = "token_test"
  }

  expect_failures = [check.region_grant_credential]
}

run "both_credentials_are_refused" {
  command = plan

  variables {
    restate              = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key      = "key_test"
    restate_access_token = "token_test"
  }

  expect_failures = [var.restate_access_token]
}

run "restate_excludes_the_per_region_inputs" {
  command = plan

  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = "key_test"
    region_name     = "prod-eu-x5z2y4qjmt"
  }

  expect_failures = [var.restate]
}

run "restate_needs_a_credential" {
  command = plan

  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu" }
    restate_api_key = null
  }

  expect_failures = [var.restate]
}

run "grant_owner_is_validated" {
  command = plan

  variables {
    region_grant    = { owner = "USER" }
    restate_api_key = "key_test"
  }

  expect_failures = [var.region_grant]
}
