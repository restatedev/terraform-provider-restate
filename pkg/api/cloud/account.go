package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// DescribeAccount reads the account this client is scoped to.
//
// It doubles as the credential validation probe: the API has no version endpoint and no whoami for a
// machine principal, so a successful read-only describe is the only available proof that the key is
// valid, account-scoped, and belongs to this account.
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

// ValidateCredentials proves the configured key can perform a read-only, account-scoped operation
// against the configured account, and reports which kind of key it is. It names the two failure modes
// an operator is most likely to hit, because "HTTP 403" does not distinguish a wrong key from a right
// key pointed at the wrong account.
//
// An organization's region grant key is not an administrator of any account, so it cannot describe
// one. It is recognised by listing the organization's region grants through the account instead.
func (c *Client) ValidateCredentials(ctx context.Context) (CredentialKind, error) {
	_, err := c.DescribeAccount(ctx)
	if err == nil {
		return CredentialAccount, nil
	}
	if api.IsUnauthorized(err) || api.IsForbidden(err) || api.IsNotFound(err) {
		_, listErr := c.RegionGrants().List(ctx, RegionGrantOwnerOrganization)
		if listErr == nil {
			return CredentialOrganizationRegionGrants, nil
		}
		// A refusal of the probe confirms the first answer. Anything else means the probe could not
		// tell, and hiding it would blame the key for an outage.
		if !api.IsUnauthorized(listErr) && !api.IsForbidden(listErr) && !api.IsNotFound(listErr) && !api.IsBadRequest(listErr) {
			return "", fmt.Errorf("the configured management API key cannot read account %s, and checking whether it is an organization key failed: %w",
				c.accountID, errors.Join(err, listErr))
		}
	}

	switch {
	case api.IsUnauthorized(err):
		return "", fmt.Errorf("the Restate Cloud management API key was rejected by %s: it is missing, malformed, or revoked: %w", c.baseURL, err)
	case api.IsForbidden(err), api.IsNotFound(err):
		// In practice both mean the same thing: this credential cannot see this account.
		return "", fmt.Errorf("the configured management API key cannot read account %s: check that the key is account-scoped with role %s and that it belongs to this account, not another one, or that it is an organization key with role %s for the account's organization: %w",
			c.accountID, RoleManagementAccess, RoleOrganizationRegionGrantAccess, err)
	default:
		return "", err
	}
}

// CredentialKind says what the configured key may reach.
type CredentialKind string

const (
	// CredentialAccount is an account's key, or an administrator's token: everything the account
	// holds.
	CredentialAccount CredentialKind = "account"
	// CredentialOrganizationRegionGrants is an organization key with role
	// OrganizationRegionGrantAccess: the organization's region grants and the regions built with them,
	// in any of its accounts, and nothing else.
	CredentialOrganizationRegionGrants CredentialKind = "organization-region-grants"
)

// GetAccountLimits reads the account's quota usage.
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
			Status:      string(derefRegionStatus(region.Status)),
		})
	}
	return regions, nil
}

func derefRegionStatus(status *generated.RegionStatus) generated.RegionStatus {
	if status == nil {
		return ""
	}
	return *status
}
