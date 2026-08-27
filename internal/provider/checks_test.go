package provider_test

import (
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// checkFake adapts an assertion about a test fake's own state into a step check.
//
// It exists because an assertion written after resource.UnitTest returns runs *after* the harness has
// destroyed everything, so it sees an empty fake and fails for the wrong reason. Anything that
// inspects the server side of a resource that is supposed to exist has to be checked from inside the
// step.
func checkFake(assert func() error) resource.TestCheckFunc {
	return func(*terraform.State) error { return assert() }
}
