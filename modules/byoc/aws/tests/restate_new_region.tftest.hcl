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

run "restate_new_region" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu", region_identity = true }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"sts.amazonaws.com\"}}"
    }
  }

  assert {
    condition     = aws_iam_openid_connect_provider.restate[0].url == "https://oidc.dev.restate.cloud" && jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Condition.StringEquals["oidc.dev.restate.cloud:sub"] == "region:rgn_x5z2y4qjmt"
    error_message = "A new region must trust its own subject."
  }
  assert {
    condition     = jsondecode(restapi_object.region[0].data).grant == { roleArn = aws_iam_role.grant.arn, permissionsBoundary = aws_iam_policy.boundary.arn }
    error_message = "SetRegionGrant must get the role and the boundary."
  }

  assert {
    condition     = jsondecode(restapi_object.reservation[0].data).identity == "region"
    error_message = "A new region must ask CreateRegion for its own identity."
  }

  assert {
    condition     = restapi_object.reservation[0].read_path == "/DescribeRegion" && jsondecode(restapi_object.reservation[0].read_data) == { name = var.restate.name }
    error_message = "The region must be read by its name, since repeating CreateRegion makes a new region once it is deleted."
  }

  assert {
    condition     = restapi_object.teardown[0].destroy_path == "/DeleteRegion" && jsondecode(restapi_object.teardown[0].destroy_data) == { regionId = restapi_object.reservation[0].id }
    error_message = "The teardown must delete the region, which keeps the grant until the region is gone."
  }
}

run "too_few_elastic_ips" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-eu", region_identity = true }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_x5z2y4qjmt"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-eu-x5z2y4qjmt\",\"restateIssuer\":\"https://oidc.dev.restate.cloud\",\"regionSubject\":\"region:rgn_x5z2y4qjmt\",\"tokenAudience\":\"sts.amazonaws.com\"}}"
    }
  }
  override_data {
    target = data.aws_servicequotas_service_quota.elastic_ips
    values = { value = 5 }
  }

  expect_failures = [check.elastic_ip_quota]
}
