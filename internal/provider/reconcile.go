package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Terraform requires the value of an attribute after an apply to equal what was planned, unless the
// plan left it unknown. So a write and a refresh reconcile state in opposite directions, and every
// resource here has to pick one deliberately:
//
//   - After a create or update, a planned value that is *known* must be kept. Adopting a different
//     value from the API fails Terraform's own consistency check with a "this is a bug in the
//     provider" message that says nothing about the real problem, which is a server that did not
//     apply the change. So the discrepancy is reported directly instead.
//   - A planned value that is *unknown* -- an Optional+Computed attribute the configuration left out,
//     say -- must be adopted, because that is the only way the resource learns what the API chose.
//   - A planned value that is *null* -- a plain Optional attribute the configuration left out -- must
//     stay null.
//   - During a refresh, the API's value is always adopted. That is what makes an out-of-band change
//     show up as drift.
//
// applyMode names the direction. reconcileString applies the rule for one attribute.
type applyMode int

const (
	applyWrite applyMode = iota
	applyRead
)

// reconcileString returns the value to store for a string attribute, and reports a discrepancy when a
// write asked for one value and the API reported another.
//
// attribute is the path used in a diagnostic, and subject names the object -- "environment env_1" --
// so the message says which thing disagreed.
func reconcileString(
	mode applyMode,
	planned types.String,
	reported string,
	attribute path.Path,
	subject string,
	diags *diag.Diagnostics,
) types.String {
	if mode == applyRead || planned.IsUnknown() {
		return types.StringValue(reported)
	}
	// A null planned value on a write means an Optional attribute the configuration left out, so it
	// must stay null: adopting the API's value would break consistency just as surely as changing a
	// known one. An Optional+Computed attribute the configuration left out is unknown, not null, and is
	// handled above.
	if planned.IsNull() {
		return planned
	}
	if planned.ValueString() != reported {
		diags.AddAttributeError(attribute,
			"The Restate API did not apply the requested value",
			fmt.Sprintf("%s was asked to set `%s` to %q, but the API reports %q.",
				subject, attribute, planned.ValueString(), reported))
	}
	return planned
}
