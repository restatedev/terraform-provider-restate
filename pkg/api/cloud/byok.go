package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// ByokRegistrations is the endpoint adapter for BYOK regions, which the customer builds and installs
// the BYOC chart in. The chart's registration with a key creates the region; the API offers no way to
// read a key back or to revoke one that no region has used.
type ByokRegistrations struct{ client *Client }

// ByokRegistrations returns the BYOK registration adapter for this client's account.
func (c *Client) ByokRegistrations() *ByokRegistrations { return &ByokRegistrations{client: c} }

// No Getter and no Updater: a key is shown once, and the region it reserves has no description until
// the chart registers.
var _ api.Creator[CreateByokRegistrationKeyInput, ByokRegistrationKey] = (*ByokRegistrations)(nil)

// Create mints a registration key and reserves the region id it will register.
//
// Retry safety is RetryThrottleOnly: a repeat after an ambiguous failure would mint a second key, and
// every key that no region has used counts against the account's limit until it expires.
func (r *ByokRegistrations) Create(ctx context.Context, input CreateByokRegistrationKeyInput) (ByokRegistrationKey, error) {
	if input.ProviderRegion == "" {
		return ByokRegistrationKey{}, errors.New("a provider region is required")
	}
	if input.Cloud != CloudGCP {
		return ByokRegistrationKey{}, fmt.Errorf("BYOK supports only %s, not %q", CloudGCP, input.Cloud)
	}

	body := generated.CreateByokRegistrationKeyJSONRequestBody{
		Cloud:          generated.CreateByokRegistrationKeyRequestCloud(input.Cloud),
		ProviderRegion: input.ProviderRegion,
	}
	if input.DisplayName != "" {
		body.RegionDisplayName = &input.DisplayName
	}

	response, err := api.Execute[generated.CreateByokRegistrationKeyResponse](ctx, r.client.executor,
		"CreateByokRegistrationKey", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.CreateByokRegistrationKey(ctx, r.client.accountID, body)
		})
	if err != nil {
		return ByokRegistrationKey{}, err
	}
	if response.RegistrationKey == "" || response.RegionId == "" {
		// The key is not in the message: a response that carries one without a region id is still a
		// credential.
		return ByokRegistrationKey{}, &api.Error{Service: api.ServiceCloud, Operation: "CreateByokRegistrationKey",
			Message: "the API reported success but returned no registration key or no region ID"}
	}
	return ByokRegistrationKey{Key: response.RegistrationKey, RegionID: response.RegionId, ExpiresAt: response.ExpiresAt}, nil
}

// DeleteRegion removes a BYOK region of the account, whatever its status, which revokes its access
// keys. The API answers 404 for a region that does not exist, including one whose key no chart has
// used yet, and 409 while the region hosts environments. A region that is already gone is success.
//
// Retry safety is RetryServerErrors: repeating the call on a deleted region answers the same.
func (r *ByokRegistrations) DeleteRegion(ctx context.Context, regionID string) error {
	if regionID == "" {
		return errors.New("a region ID is required")
	}
	_, err := api.Execute[generated.DeleteByokRegionResponse](ctx, r.client.executor,
		"DeleteByokRegion", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.DeleteByokRegion(ctx, r.client.accountID,
				generated.DeleteByokRegionJSONRequestBody{RegionId: regionID})
		})
	if api.IsNotFound(err) {
		return nil
	}
	return err
}
