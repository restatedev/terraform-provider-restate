# The provider's admin_url already scopes the Admin API to one environment, so a bare deployment ID
# is enough.
#
# `force`, `breaking`, `overwrite` and `use_http_11` cannot be imported: the Admin API reports none
# of them. They are set to their defaults and the provider warns about it, so compare
# `terraform plan` against your configuration before applying. `force_destroy` is also false, so a
# destroy will refuse until you set it deliberately.
terraform import restate_deployment.orders dp_1a2b3c
