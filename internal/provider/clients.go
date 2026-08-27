package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// requireCloudClient reports a specific, actionable diagnostic when a Cloud resource or data source
// is used by a provider instance that has no Cloud credentials.
//
// The provider deliberately allows a Cloud-only, Admin-only, or combined configuration, so the
// absence of a client is not a configuration error in itself. It becomes one only when something
// actually needs it, and this is where that is said.
func requireCloudClient(data *providerData, typeName string, diags *diag.Diagnostics) bool {
	if data != nil && data.Cloud != nil {
		return true
	}
	diags.AddError(
		fmt.Sprintf("%s requires Restate Cloud Management API configuration", typeName),
		fmt.Sprintf("This provider instance has no Cloud Management API credentials, so it cannot manage %s.\n\n"+
			"Set `account_id` and `management_api_key` on the provider, or the `%s` and `%s` environment "+
			"variables. If this configuration manages more than one account or environment, use a provider alias "+
			"and set `provider = restate.<alias>` on the resource.",
			typeName, envCloudAccountID, envManagementAPIKey))
	return false
}

// requireAdminClient is the Admin API counterpart of requireCloudClient.
func requireAdminClient(data *providerData, typeName string, diags *diag.Diagnostics) bool {
	if data != nil && data.Admin != nil {
		return true
	}
	diags.AddError(
		fmt.Sprintf("%s requires Restate Admin API configuration", typeName),
		fmt.Sprintf("This provider instance has no environment Admin API credentials, so it cannot manage %s.\n\n"+
			"Set `admin_url` and `environment_api_key` on the provider, or the `%s` and `%s` environment "+
			"variables. An environment's admin URL and API key cannot come from resources in the same apply; see "+
			"the two-stage bootstrap guide.",
			typeName, envAdminURL, envEnvironmentAPIKey))
	return false
}

// configureProviderData extracts the shared provider data a resource or data source receives.
//
// A nil ProviderData means the framework has not configured the provider yet, which happens during
// validation. Returning nil is correct there: the read or apply that follows will receive the real
// value.
func configureProviderData(raw any, diags *diag.Diagnostics) *providerData {
	if raw == nil {
		return nil
	}
	data, ok := raw.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *providerData, got %T. This is a bug in the provider.", raw))
		return nil
	}
	return data
}
