package cloud

import (
	"context"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// DescribeAccount reads the account this client is scoped to.
//
// This is also the credential validation probe: the Cloud Management API has no version endpoint
// and no whoami for a machine principal, so a successful read-only account describe is the only
// available proof that the key is valid, is account-scoped, and belongs to this account.
func (c *Client) DescribeAccount(ctx context.Context) (Account, error) {
	response, err := api.Execute[generated.DescribeAccountResponse](ctx, c.executor, "DescribeAccount", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return c.generated.DescribeAccount(ctx, c.accountID, generated.DescribeAccountJSONRequestBody{})
		})
	if err != nil {
		return Account{}, err
	}

	account := Account{ID: c.accountID}
	for _, environment := range response.Environments {
		account.Environments = append(account.Environments, EnvironmentSummary{
			ID:     environment.EnvironmentId,
			Name:   environment.Name,
			Region: environment.Region,
			Tier:   Tier(environment.TierId),
		})
	}
	if response.BillingMode != nil {
		account.BillingMode = BillingMode(*response.BillingMode)
	}
	if response.Subscription != nil {
		account.Subscription = &Subscription{
			Status: SubscriptionStatus(response.Subscription.Status),
			// The contract types these as numbers; they are Unix seconds.
			CurrentPeriodStart: int64(response.Subscription.CurrentPeriodStart),
			CurrentPeriodEnd:   int64(response.Subscription.CurrentPeriodEnd),
		}
	}
	return account, nil
}

// ValidateCredentials proves the configured management key can perform a read-only, account-scoped
// operation against the configured account.
//
// It translates the two failure modes an operator is most likely to hit into diagnostics that name
// the actual cause, because "HTTP 403" alone does not distinguish a wrong key from a right key
// pointed at the wrong account.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	_, err := c.DescribeAccount(ctx)
	if err == nil {
		return nil
	}

	switch {
	case api.IsUnauthorized(err):
		return fmt.Errorf("the Restate Cloud management API key was rejected by %s: it is missing, malformed, or revoked: %w", c.baseURL, err)
	case api.IsForbidden(err), api.IsNotFound(err):
		// Both shapes mean the same thing in practice: this credential cannot see this account.
		return fmt.Errorf("the configured management API key cannot read account %s: check that the key is account-scoped with role %s and that it belongs to this account, not another one: %w",
			c.accountID, RoleManagementAccess, err)
	default:
		return err
	}
}

// GetAccountLimits reads the account's quota usage. It is read-only; the provider never manages
// limits.
func (c *Client) GetAccountLimits(ctx context.Context) (AccountLimits, error) {
	response, err := api.Execute[generated.GetAccountLimitsResponse](ctx, c.executor, "GetAccountLimits", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return c.generated.GetAccountLimits(ctx, c.accountID, generated.GetAccountLimitsJSONRequestBody{})
		})
	if err != nil {
		return AccountLimits{}, err
	}

	limits := AccountLimits{
		FreeEnvironments: Limit{
			Usage: float64(response.FreeEnvironments.Usage),
			Limit: float64(response.FreeEnvironments.Limit),
		},
	}
	if response.TierLimits != nil {
		limits.TierLimits = make(map[string]Limit, len(*response.TierLimits))
		for tier, limit := range *response.TierLimits {
			limits.TierLimits[tier] = Limit{Usage: float64(limit.Usage), Limit: float64(limit.Limit)}
		}
	}
	return limits, nil
}

// ListRegions enumerates the regions this account may place environments in.
func (c *Client) ListRegions(ctx context.Context) ([]Region, error) {
	response, err := api.Execute[generated.ListRegionsResponse](ctx, c.executor, "ListRegions", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return c.generated.ListRegions(ctx, c.accountID, generated.ListRegionsJSONRequestBody{})
		})
	if err != nil {
		return nil, err
	}

	regions := make([]Region, 0, len(response.Regions))
	for _, region := range response.Regions {
		regions = append(regions, Region{
			Name:        region.RegionName,
			DisplayName: derefString(region.RegionDisplayName),
		})
	}
	return regions, nil
}
