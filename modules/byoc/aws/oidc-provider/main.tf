# The IAM OIDC identity provider for Restate's issuer, which every region grant in the account trusts.
#
# AWS allows one provider for each issuer in an account, and destroying it cuts off every grant that
# trusts it, so it has a state of its own. A region grant module never creates or deletes it and
# takes its ARN as oidc_provider_arn. Keep this state apart from the grants' states, and do not
# destroy it while any grant in the account is in use.
#
# A destroy of this state is refused, since it would cut off every grant. To remove the provider on
# purpose, delete the lifecycle block below, apply, and then destroy.
#
# There is no thumbprint: IAM verifies the issuer's certificate chain against its own trust store.

resource "aws_iam_openid_connect_provider" "restate" {
  url            = var.restate_issuer
  client_id_list = [var.token_audience]

  lifecycle {
    prevent_destroy = true
  }
}
