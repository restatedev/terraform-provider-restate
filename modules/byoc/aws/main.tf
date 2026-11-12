# The role Restate Cloud's composition cluster assumes to build a BYOC region in this account
# (credentials.source Grant). Its policy mirrors what the region's own Crossplane role holds on the
# ProviderConfig path, scoped to the region's names. That role is defined in restatedev/restate-cloud,
# in byoc/functions/go-templating-dir/templates/byoc-infra/aws/crossplane-controller-role.yaml.tmpl.
#
# A region signs in either with its own identity, a token from Restate's issuer whose subject names
# the region, or through the composition cluster's roles with the region's external id. Restate
# Cloud chooses when it creates the region, and only the trust policy differs between the two.
#
# - It may create or change a role only when that role carries the boundary below, by its exact
#   ARN, so no role the region creates can do more than the boundary allows.
# - It can never write the boundary. This module owns it, so a grant cannot write a second, wider
#   boundary and create a role under that one instead.
# - It can never change this role or its policy.
#
# With `region_grant` (restate.tf) the role serves every region of one Restate region grant. It
# trusts the grant's subject, `grant:<grant id>`, and every name it covers starts with the grant's
# resource prefix. The issuer's OIDC provider is never created here, because one provider serves
# every grant in the account and gets a state of its own (oidc-provider/).

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_region" "current" {}

locals {
  account = data.aws_caller_identity.current.account_id
  # Restate Cloud's answer when it created the region (restate.tf), or what was passed in. A region
  # that signs in with its own identity has an issuer and a subject, and no external id or principals.
  # A region grant answers the same issuer and audience, with the grant's subject and resource prefix.
  external_id        = var.restate != null ? try(local.grant_inputs.externalId, null) : var.external_id
  region_name        = local.region_grant_mode ? local.region_grant_inputs.resourcePrefix : (var.restate != null ? local.grant_inputs.regionName : var.region_name)
  restate_principals = var.restate != null ? try(local.grant_inputs.restatePrincipals, null) : var.restate_principals
  restate_issuer     = local.region_grant_mode ? local.region_grant_inputs.restateIssuer : (var.restate != null ? try(local.grant_inputs.restateIssuer, null) : var.restate_issuer)
  region_subject     = local.region_grant_mode ? local.region_grant_inputs.grantSubject : (var.restate != null ? try(local.grant_inputs.regionSubject, null) : var.region_subject)
  token_audience     = local.region_grant_mode ? try(local.region_grant_inputs.tokenAudience, var.token_audience) : (var.restate != null ? try(local.grant_inputs.tokenAudience, var.token_audience) : var.token_audience)
  # Known when Terraform plans, because it decides whether the identity provider exists (restate.tf).
  # A region grant always signs in with the grant's identity, and only ever uses the provider it is given.
  region_identity   = local.region_grant_mode || (var.restate != null ? local.restate_region_identity : var.restate_issuer != null)
  oidc_provider_arn = local.region_grant_mode ? var.oidc_provider_arn : (local.region_identity ? coalesce(var.oidc_provider_arn, one(aws_iam_openid_connect_provider.restate[*].arn)) : null)
  iam               = "arn:${data.aws_partition.current.partition}:iam::${local.account}"
  # A grant's names carry its id, so a second grant in the account, a grant made again while the
  # last one's names are still in use, or a region from the older modes cannot collide with it.
  role_name = coalesce(var.role_name, local.region_grant_mode ? "restate-cloud-${local.region_grant_inputs.resourcePrefix}" : "restate-cloud-byoc")
  # A region's names start with its name. A region grant's regions are named <prefix>-<region part>,
  # so the hyphen keeps the match to names the composition builds under that prefix.
  prefix = local.region_grant_mode ? "${local.region_name}-*" : "${local.region_name}*"
  bucket = "arn:${data.aws_partition.current.partition}:s3:::${local.prefix}-snapshots-${local.account}"

  # EKS creates one IAM OIDC provider for each cluster, in any AWS region. A region grant serves
  # regions in more than one AWS region, and a cloud account with a region grant may also run the
  # customer's own EKS clusters, which are told apart only by the tag below.
  eks_providers = "${local.iam}:oidc-provider/oidc.eks.*.amazonaws.com/id/*"
  grant_tag     = "restate.cloud/region-grant"
  # What a region grant may do with EKS OIDC providers, as far as IAM can express it. Create and
  # Tag are separate actions, and creating a provider with tags needs iam:TagOpenIDConnectProvider
  # as well. aws:RequestTag/<key> holds the tags of the create call, and upjet adds its own
  # crossplane-* tags to them, so the tag keys are not limited to a list. IAM has no key that tells
  # the tag check inside a create from a tag call on an existing provider, so the role can also tag
  # an untagged provider with the grant's id. The denies keep a tagged provider's tag fixed, and the
  # upkeep statement reaches only providers that already carry it.
  region_grant_eks_statements = [
    {
      Sid       = "ClusterIssuerCreate"
      Effect    = "Allow"
      Action    = ["iam:CreateOpenIDConnectProvider", "iam:TagOpenIDConnectProvider"]
      Resource  = local.eks_providers
      Condition = { StringEquals = { "aws:RequestTag/${local.grant_tag}" = local.region_grant_id } }
    },
    {
      Sid       = "ClusterIssuerUpkeep"
      Effect    = "Allow"
      Action    = ["iam:DeleteOpenIDConnectProvider", "iam:TagOpenIDConnectProvider", "iam:UntagOpenIDConnectProvider", "iam:AddClientIDToOpenIDConnectProvider", "iam:RemoveClientIDFromOpenIDConnectProvider", "iam:UpdateOpenIDConnectProviderThumbprint"]
      Resource  = local.eks_providers
      Condition = { StringEquals = { "aws:ResourceTag/${local.grant_tag}" = local.region_grant_id } }
    },
    {
      # Sending the grant's key to a provider that already has it would rewrite the tag. The first
      # tag on a provider carries no resource tag yet, so this never matches inside a create.
      Sid      = "DenyGrantTagRewrite"
      Effect   = "Deny"
      Action   = ["iam:TagOpenIDConnectProvider"]
      Resource = local.eks_providers
      Condition = {
        "ForAnyValue:StringEquals" = { "aws:TagKeys" = [local.grant_tag] }
        Null                       = { "aws:ResourceTag/${local.grant_tag}" = "false" }
      }
    },
    {
      Sid       = "DenyGrantTagRemoval"
      Effect    = "Deny"
      Action    = ["iam:UntagOpenIDConnectProvider"]
      Resource  = local.eks_providers
      Condition = { "ForAnyValue:StringEquals" = { "aws:TagKeys" = [local.grant_tag] } }
    },
  ]
  # Only the region's own AWS region, because a region-scoped grant serves exactly one region.
  region_scoped_eks_statements = [
    {
      Sid      = "ClusterIssuer"
      Effect   = "Allow"
      Action   = ["iam:CreateOpenIDConnectProvider", "iam:DeleteOpenIDConnectProvider", "iam:TagOpenIDConnectProvider", "iam:UntagOpenIDConnectProvider", "iam:AddClientIDToOpenIDConnectProvider", "iam:RemoveClientIDFromOpenIDConnectProvider", "iam:UpdateOpenIDConnectProviderThumbprint"]
      Resource = "${local.iam}:oidc-provider/oidc.eks.${data.aws_region.current.region}.amazonaws.com/id/*"
    },
  ]
}

# The most any role the region creates may do. Kept equal to byoc-infra/aws/permissions-boundary.yaml.tmpl
# in restatedev/restate-cloud, which a region on the ProviderConfig path composes for itself. It grants no IAM write except PassRole
# on the region's own roles and service-linked roles, so no role under it can change any grant.
resource "aws_iam_policy" "boundary" {
  name        = "${local.role_name}-boundary"
  description = local.region_grant_mode ? "The most any role in a BYOC region of region grant ${local.region_grant_id} may do" : "The most any role in BYOC region ${local.region_name} may do"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "Infrastructure"
        Effect   = "Allow"
        Action   = ["ec2:*", "eks:*", "elasticloadbalancing:*", "autoscaling:Describe*"]
        Resource = "*"
      },
      {
        # The node role's AmazonEKSWorkerNodePolicy grants this to the eks-pod-identity-agent.
        Sid      = "PodIdentity"
        Effect   = "Allow"
        Action   = ["eks-auth:AssumeRoleForPodIdentity"]
        Resource = "*"
      },
      {
        Sid      = "ImagePull"
        Effect   = "Allow"
        Action   = ["ecr:GetAuthorizationToken", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"]
        Resource = "*"
      },
      {
        Sid      = "Lookups"
        Effect   = "Allow"
        Action   = ["ssm:GetParameter", "ssm:GetParameters", "pricing:GetProducts", "iam:Get*", "iam:List*"]
        Resource = "*"
      },
      {
        Sid      = "ClusterLogsAndKeys"
        Effect   = "Allow"
        Action   = ["logs:CreateLogGroup", "logs:PutRetentionPolicy", "logs:DescribeLogGroups", "kms:CreateGrant", "kms:DescribeKey"]
        Resource = "*"
      },
      {
        Sid      = "SnapshotBucket"
        Effect   = "Allow"
        Action   = ["s3:*"]
        Resource = [local.bucket, "${local.bucket}/*"]
      },
      {
        Sid      = "PassRegionRoles"
        Effect   = "Allow"
        Action   = ["iam:PassRole"]
        Resource = "${local.iam}:role/${local.prefix}"
      },
      {
        Sid       = "ServiceLinkedRoles"
        Effect    = "Allow"
        Action    = ["iam:CreateServiceLinkedRole"]
        Resource  = "*"
        Condition = { StringEquals = { "iam:AWSServiceName" = ["eks.amazonaws.com", "eks-fargate.amazonaws.com", "elasticloadbalancing.amazonaws.com", "spot.amazonaws.com"] } }
      },
    ]
  })

  # The grant's resources are replaced without removing the old ones first (restate.tf).
  lifecycle {
    create_before_destroy = true
  }
}

# AWS allows one provider for each issuer in an account, so a second region's grant passes
# oidc_provider_arn instead of creating another. A region grant never creates it (oidc-provider/).
resource "aws_iam_openid_connect_provider" "restate" {
  count = local.region_identity && var.oidc_provider_arn == null && !local.region_grant_mode ? 1 : 0

  url            = local.restate_issuer
  client_id_list = [local.token_audience]

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_iam_role" "grant" {
  name        = local.role_name
  description = local.region_grant_mode ? "Restate Cloud builds the BYOC regions of region grant ${local.region_grant_id} through this role" : "Restate Cloud builds BYOC region ${local.region_name} through this role"

  # With its own identity, the region signs in with a token from Restate's issuer whose subject
  # names the region. Otherwise the composition cluster's roles assume the role with the region's
  # external id.
  assume_role_policy = local.region_identity ? jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect    = "Allow"
        Principal = { Federated = local.oidc_provider_arn }
        Action    = ["sts:AssumeRoleWithWebIdentity"]
        Condition = {
          StringEquals = {
            "${trimprefix(local.restate_issuer, "https://")}:aud" = local.token_audience
            "${trimprefix(local.restate_issuer, "https://")}:sub" = local.region_subject
          }
        }
      },
    ]
    }) : jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect    = "Allow"
        Principal = { AWS = local.restate_principals }
        Action    = ["sts:AssumeRole"]
        Condition = { StringEquals = { "sts:ExternalId" = local.external_id } }
      },
      {
        # EKS Pod Identity sessions carry session tags, which an assume passes along. A TagSession
        # request carries no external id, so it gets its own statement; on its own it grants
        # nothing, because the AssumeRole above still needs the external id.
        Effect    = "Allow"
        Principal = { AWS = local.restate_principals }
        Action    = ["sts:TagSession"]
      },
    ]
  })

  lifecycle {
    create_before_destroy = true

    precondition {
      condition     = !startswith(local.role_name, local.region_name)
      error_message = "role_name must not start with ${local.region_grant_mode ? "the grant's resource prefix" : "region_name"}, or the grant could change its own role."
    }

    precondition {
      condition     = !local.region_grant_mode || var.oidc_provider_arn != null
      error_message = "A region grant needs oidc_provider_arn, the ARN of the account's IAM OIDC provider for Restate's issuer. Create it once with the oidc-provider module, and see the README for importing one that already exists."
    }

    precondition {
      condition     = (!local.region_grant_mode || var.oidc_provider_arn == null) ? true : endswith(var.oidc_provider_arn, "/${trimprefix(local.restate_issuer, "https://")}")
      error_message = "oidc_provider_arn must be the provider for Restate's issuer ${local.restate_issuer == null ? "" : local.restate_issuer}, or no token would be accepted."
    }
  }
}

resource "aws_iam_role_policy" "grant" {
  name = "restate-cloud-byoc"
  role = aws_iam_role.grant.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat([
      {
        Sid      = "Infrastructure"
        Effect   = "Allow"
        Action   = ["ec2:*", "eks:*", "elasticloadbalancing:*"]
        Resource = "*"
      },
      {
        Sid      = "SnapshotBucket"
        Effect   = "Allow"
        Action   = ["s3:*"]
        Resource = [local.bucket, "${local.bucket}/*"]
      },
      {
        Sid      = "ListBuckets"
        Effect   = "Allow"
        Action   = ["s3:ListAllMyBuckets"]
        Resource = "*"
      },
      {
        Sid      = "IamRead"
        Effect   = "Allow"
        Action   = ["iam:Get*", "iam:List*"]
        Resource = "*"
      },
      {
        Sid       = "BoundedRoles"
        Effect    = "Allow"
        Action    = ["iam:CreateRole", "iam:PutRolePolicy", "iam:DeleteRolePolicy", "iam:AttachRolePolicy", "iam:DetachRolePolicy", "iam:PutRolePermissionsBoundary"]
        Resource  = "${local.iam}:role/${local.prefix}"
        Condition = { StringEquals = { "iam:PermissionsBoundary" = aws_iam_policy.boundary.arn } }
      },
      {
        Sid      = "RoleUpkeep"
        Effect   = "Allow"
        Action   = ["iam:DeleteRole", "iam:TagRole", "iam:UntagRole", "iam:UpdateRole", "iam:UpdateRoleDescription", "iam:UpdateAssumeRolePolicy"]
        Resource = "${local.iam}:role/${local.prefix}"
      },
      {
        Sid      = "RegionPolicies"
        Effect   = "Allow"
        Action   = ["iam:CreatePolicy", "iam:DeletePolicy", "iam:CreatePolicyVersion", "iam:DeletePolicyVersion", "iam:SetDefaultPolicyVersion", "iam:TagPolicy", "iam:UntagPolicy"]
        Resource = "${local.iam}:policy/${local.prefix}"
      },
      {
        Sid      = "InstanceProfiles"
        Effect   = "Allow"
        Action   = ["iam:CreateInstanceProfile", "iam:DeleteInstanceProfile", "iam:AddRoleToInstanceProfile", "iam:RemoveRoleFromInstanceProfile", "iam:TagInstanceProfile", "iam:UntagInstanceProfile"]
        Resource = "${local.iam}:instance-profile/${local.prefix}"
      },
      ],
      [for statement in local.region_grant_eks_statements : statement if local.region_grant_mode],
      [for statement in local.region_scoped_eks_statements : statement if !local.region_grant_mode],
      [
        {
          Sid      = "PassRegionRoles"
          Effect   = "Allow"
          Action   = ["iam:PassRole"]
          Resource = "${local.iam}:role/${local.prefix}"
        },
        {
          Sid       = "ServiceLinkedRoles"
          Effect    = "Allow"
          Action    = ["iam:CreateServiceLinkedRole"]
          Resource  = "*"
          Condition = { StringEquals = { "iam:AWSServiceName" = ["eks.amazonaws.com", "eks-fargate.amazonaws.com", "elasticloadbalancing.amazonaws.com", "spot.amazonaws.com"] } }
        },
        {
          Sid      = "DenyOwnGrant"
          Effect   = "Deny"
          Action   = ["iam:Create*", "iam:Delete*", "iam:Put*", "iam:Attach*", "iam:Detach*", "iam:Update*", "iam:Tag*", "iam:Untag*", "iam:SetDefaultPolicyVersion", "iam:PassRole"]
          Resource = aws_iam_role.grant.arn
        },
        {
          # RegionPolicies cannot name the boundary already, because role_name does not start with
          # region_name. The deny keeps it so if either changes.
          Sid      = "DenyBoundaryWrites"
          Effect   = "Deny"
          Action   = ["iam:CreatePolicy", "iam:DeletePolicy", "iam:CreatePolicyVersion", "iam:DeletePolicyVersion", "iam:SetDefaultPolicyVersion", "iam:TagPolicy", "iam:UntagPolicy"]
          Resource = aws_iam_policy.boundary.arn
        },
        {
          Sid      = "DenyBoundaryRemoval"
          Effect   = "Deny"
          Action   = ["iam:DeleteRolePermissionsBoundary"]
          Resource = "*"
        },
    ])
  })

  lifecycle {
    create_before_destroy = true
  }
}
