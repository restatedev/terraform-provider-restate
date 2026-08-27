# Imported by service name.
#
# An import adopts every current setting, including values that came from SDK discovery rather than
# from anyone's Terraform configuration. Set only the attributes you actually want to manage; any you
# leave out are not managed and not reverted, and the first plan will show them being dropped from
# state, which changes nothing on the server.
terraform import restate_service_configuration.order_service OrderService
