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

# A region that signs in through the composition cluster, planned again with region_identity unset
# as an older version of the module left it: the identity comes from the region in the state.
run "restate_composition_region" {
  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-us", region_identity = false }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_aaaaaaaaaa"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-us-aaaaaaaaaa\",\"externalId\":\"rgn_aaaaaaaaaa\",\"restatePrincipals\":[\"arn:aws:iam::111111111111:role/restate-composition-provider\"]}}"
    }
  }
}

run "restate_composition_region_planned_again" {
  command = plan

  variables {
    restate         = { account_id = "acc_1a2b3c4d5e", name = "prod-us" }
    restate_api_key = "key_test"
  }

  override_resource {
    target = restapi_object.reservation
    values = {
      id           = "rgn_aaaaaaaaaa"
      api_response = "{\"grantInputs\":{\"regionName\":\"prod-us-aaaaaaaaaa\",\"externalId\":\"rgn_aaaaaaaaaa\",\"restatePrincipals\":[\"arn:aws:iam::111111111111:role/restate-composition-provider\"]}}"
    }
  }

  assert {
    condition     = length(aws_iam_openid_connect_provider.restate) == 0 && jsondecode(aws_iam_role.grant.assume_role_policy).Statement[0].Condition.StringEquals["sts:ExternalId"] == "rgn_aaaaaaaaaa"
    error_message = "An existing region must keep the composition cluster's trust."
  }

  assert {
    condition     = !contains(keys(jsondecode(restapi_object.reservation[0].data)), "identity")
    error_message = "A region that was not asked to have its own identity must not send one."
  }
}
