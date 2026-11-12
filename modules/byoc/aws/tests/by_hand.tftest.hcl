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
  mock_resource "aws_iam_openid_connect_provider" {
    defaults = { arn = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud" }
  }
}

mock_provider "restapi" {}

variables {
  # terraform test fails an apply run whose ephemeral variable is unset, so the key the
  # module ignores without `restate` is set here. A real plan and apply need nothing.
  restate_api_key = "key_unused"
  region_name     = "prod-eu-x5z2y4qjmt"
}

# The trust policy of a grant made before regions had their own identity, exactly as it was.
run "composition_by_hand" {
  variables {
    external_id        = "rgn_x5z2y4qjmt"
    restate_principals = ["arn:aws:iam::111111111111:role/restate-composition-provider"]
  }

  assert {
    condition = aws_iam_role.grant.assume_role_policy == jsonencode({
      Version = "2012-10-17"
      Statement = [
        {
          Effect    = "Allow"
          Principal = { AWS = ["arn:aws:iam::111111111111:role/restate-composition-provider"] }
          Action    = ["sts:AssumeRole"]
          Condition = { StringEquals = { "sts:ExternalId" = "rgn_x5z2y4qjmt" } }
        },
        {
          Effect    = "Allow"
          Principal = { AWS = ["arn:aws:iam::111111111111:role/restate-composition-provider"] }
          Action    = ["sts:TagSession"]
        },
      ]
    })
    error_message = "The composition cluster's trust policy changed."
  }
  assert {
    condition     = length(aws_iam_openid_connect_provider.restate) == 0 && output.oidc_provider_arn == null
    error_message = "A grant through the composition cluster needs no identity provider."
  }
  assert {
    condition     = keys(output.grant) == ["permissionsBoundary", "roleArn"] && output.external_id == "rgn_x5z2y4qjmt"
    error_message = "grant must hold only what SetRegionGrant accepts."
  }
}

run "region_identity_by_hand" {
  variables {
    restate_issuer = "https://oidc.restate.cloud"
    region_subject = "region:rgn_x5z2y4qjmt"
  }

  assert {
    condition = aws_iam_role.grant.assume_role_policy == jsonencode({
      Version = "2012-10-17"
      Statement = [{
        Effect    = "Allow"
        Principal = { Federated = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud" }
        Action    = ["sts:AssumeRoleWithWebIdentity"]
        Condition = {
          StringEquals = {
            "oidc.restate.cloud:aud" = "sts.amazonaws.com"
            "oidc.restate.cloud:sub" = "region:rgn_x5z2y4qjmt"
          }
        }
      }]
    })
    error_message = "The trust policy must accept only the region's subject from Restate's issuer."
  }
  assert {
    condition     = aws_iam_openid_connect_provider.restate[0].url == "https://oidc.restate.cloud" && one(aws_iam_openid_connect_provider.restate[0].client_id_list) == "sts.amazonaws.com"
    error_message = "The identity provider must be for Restate's issuer and STS."
  }
  assert {
    condition     = output.external_id == null && output.oidc_provider_arn == "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud"
    error_message = "A region with its own identity has no external id."
  }
}

run "second_region_reuses_the_provider" {
  variables {
    restate_issuer    = "https://oidc.restate.cloud"
    region_subject    = "region:rgn_abcdefghij"
    oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/oidc.restate.cloud"
  }

  assert {
    condition     = length(aws_iam_openid_connect_provider.restate) == 0 && jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Principal.Federated == var.oidc_provider_arn
    error_message = "With oidc_provider_arn the grant must trust that provider and create none."
  }
}
