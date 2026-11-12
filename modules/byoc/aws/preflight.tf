# A region takes one Elastic IP for the NAT gateway and one for the entry address in each of its
# three zones, and AWS allows five in a region by default. Without enough room the build stops at
# CREATING_INFRASTRUCTURE. A check only warns, so a caller without servicequotas:GetServiceQuota
# sees a warning and the apply goes on.
check "elastic_ip_quota" {
  data "aws_servicequotas_service_quota" "elastic_ips" {
    service_code = "ec2"
    quota_code   = "L-0263D0A3"
  }

  assert {
    condition     = data.aws_servicequotas_service_quota.elastic_ips.value >= 6
    error_message = "The region needs 6 Elastic IPs in ${data.aws_region.current.region}, but the account allows ${data.aws_servicequotas_service_quota.elastic_ips.value}. Ask AWS to raise quota L-0263D0A3 to at least 6, or the build stops at CREATING_INFRASTRUCTURE."
  }
}
