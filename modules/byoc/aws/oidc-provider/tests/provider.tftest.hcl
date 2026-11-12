mock_provider "aws" {}

# The provider cannot be destroyed, and a test that applies would fail when it cleans up, so this plans.
run "creates_the_issuers_provider" {
  command = plan

  variables {
    restate_issuer = "https://oidc.restate.cloud"
  }


  assert {
    condition     = aws_iam_openid_connect_provider.restate.url == "https://oidc.restate.cloud" && aws_iam_openid_connect_provider.restate.client_id_list == toset(["sts.amazonaws.com"])
    error_message = "The provider must trust the issuer with the AWS audience."
  }
}

run "rejects_an_issuer_that_is_not_https" {
  command = plan

  variables {
    restate_issuer = "oidc.restate.cloud"
  }

  expect_failures = [var.restate_issuer]
}
