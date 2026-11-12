package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// RegionGrants is the endpoint adapter for region grants.
type RegionGrants struct{ client *Client }

// RegionGrants returns the region grants adapter. Grants of the account and of its organization are
// both reached through the account in the path.
func (c *Client) RegionGrants() *RegionGrants { return &RegionGrants{client: c} }

// A grant has no update operation: its cloud account and owner are its identity, and Restate derives
// its inputs. Confirm is a separate step with its own method because it is the one write that touches
// the customer's cloud.
var (
	_ api.Creator[CreateRegionGrantInput, RegionGrant] = (*RegionGrants)(nil)
	_ api.Getter[string, RegionGrant]                  = (*RegionGrants)(nil)
	_ api.Deleter[string, struct{}]                    = (*RegionGrants)(nil)
)

// Reasons a PENDING grant reports for itself.
const (
	GrantAwaitingConfirmation = "AWAITING_CONFIRMATION"
	GrantConfirming           = "CONFIRMING"
	GrantConfirmationFailed   = "CONFIRMATION_FAILED"
)

// ErrRegionGrantGone reports that a region grant was deleted or expired, by a 404 or by its status.
var ErrRegionGrantGone = errors.New("the region grant no longer exists")

// Create creates a region grant, or answers the owner's live grant for the same cloud account.
//
// Retry safety is RetryServerErrors: a repeat answers the grant the first request created while it
// lives, so a retry cannot create a second one.
func (g *RegionGrants) Create(ctx context.Context, input CreateRegionGrantInput) (RegionGrant, error) {
	body := generated.CreateRegionGrantJSONRequestBody{Cloud: generated.CreateRegionGrantRequestCloud(input.Cloud)}
	if input.Owner != "" {
		owner := generated.RegionGrantOwner(input.Owner)
		body.Owner = &owner
	}
	if input.DisplayName != "" {
		body.DisplayName = &input.DisplayName
	}
	switch input.Cloud {
	case CloudAWS:
		body.Aws = &struct {
			AccountId string `json:"accountId"`
		}{AccountId: input.Account.AWSAccountID}
	case CloudGCP:
		body.Gcp = &struct {
			ProjectId string `json:"projectId"`
		}{ProjectId: input.Account.GCPProjectID}
	case CloudAzure:
		var subscription openapi_types.UUID
		if err := subscription.UnmarshalText([]byte(input.Account.AzureSubscriptionID)); err != nil {
			return RegionGrant{}, fmt.Errorf("azure subscription id %q is not a UUID: %w", input.Account.AzureSubscriptionID, err)
		}
		body.Azure = &struct {
			SubscriptionId openapi_types.UUID `json:"subscriptionId"`
		}{SubscriptionId: subscription}
	default:
		return RegionGrant{}, fmt.Errorf("unknown cloud %q", input.Cloud)
	}
	if input.Account.ID() == "" {
		return RegionGrant{}, fmt.Errorf("a %s cloud account is required", input.Cloud)
	}

	create := func() (generated.RegionGrantDescription, error) {
		return api.Execute[generated.RegionGrantDescription](ctx, g.client.executor, "CreateRegionGrant", api.RetryServerErrors,
			func(ctx context.Context) (*http.Response, error) {
				return g.client.generated.CreateRegionGrant(ctx, g.client.accountID, body)
			})
	}
	response, err := create()
	// A 410 means the owner's grant for this cloud account ended while the request ran, and the
	// contract says to try again, which creates a new one.
	if api.IsGone(err) {
		response, err = create()
	}
	if err != nil {
		return RegionGrant{}, err
	}
	if response.GrantId == "" {
		return RegionGrant{}, &api.Error{Service: api.ServiceCloud, Operation: "CreateRegionGrant",
			Message: "the API reported success but returned no grant ID"}
	}
	return regionGrantFromResponse(response), nil
}

// Get describes one region grant, whatever its status.
func (g *RegionGrants) Get(ctx context.Context, grantID string) (RegionGrant, error) {
	if grantID == "" {
		return RegionGrant{}, errors.New("a region grant ID is required")
	}
	response, err := api.Execute[generated.RegionGrantDescription](ctx, g.client.executor, "DescribeRegionGrant", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return g.client.generated.DescribeRegionGrant(ctx, g.client.accountID,
				generated.DescribeRegionGrantJSONRequestBody{GrantId: grantID})
		})
	if err != nil {
		return RegionGrant{}, err
	}
	return regionGrantFromResponse(response), nil
}

// GetLive collapses a 404, EXPIRED and DELETED into ErrRegionGrantGone. The grant is returned with
// the error when the API still describes it.
func (g *RegionGrants) GetLive(ctx context.Context, grantID string) (RegionGrant, error) {
	grant, err := g.Get(ctx, grantID)
	if err != nil {
		if api.IsNotFound(err) {
			return RegionGrant{}, fmt.Errorf("%w: %s", ErrRegionGrantGone, grantID)
		}
		return RegionGrant{}, err
	}
	if grant.Status.Ended() {
		return grant, fmt.Errorf("%w: %s is %s", ErrRegionGrantGone, grantID, grant.Status)
	}
	return grant, nil
}

// List enumerates the live grants of one owner. An organization's key cannot list an account's
// grants, and an account's key cannot list its organization's.
func (g *RegionGrants) List(ctx context.Context, owner RegionGrantOwner) ([]RegionGrant, error) {
	body := generated.ListRegionGrantsJSONRequestBody{}
	if owner != "" {
		wire := generated.RegionGrantOwner(owner)
		body.Owner = &wire
	}
	response, err := api.Execute[generated.ListRegionGrantsResponse](ctx, g.client.executor, "ListRegionGrants", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return g.client.generated.ListRegionGrants(ctx, g.client.accountID, body)
		})
	if err != nil {
		return nil, err
	}
	grants := make([]RegionGrant, 0, len(response.Grants))
	for _, grant := range response.Grants {
		grants = append(grants, regionGrantFromResponse(grant))
	}
	return grants, nil
}

// Confirm hands the grant module's `grant` output to Restate, unchanged, and answers at once. Restate
// then signs in through it in the background, and the grant turns READY or reports
// CONFIRMATION_FAILED; WaitUntilConfirmed follows that. A 400 means the output itself is invalid.
//
// Retry safety is RetryServerErrors: a repeat with the same output while the grant is CONFIRMING or
// READY changes nothing, and another output for a READY grant is refused.
func (g *RegionGrants) Confirm(ctx context.Context, grantID string, grant map[string]string) (RegionGrant, error) {
	if grantID == "" {
		return RegionGrant{}, errors.New("a region grant ID is required")
	}
	if len(grant) == 0 {
		return RegionGrant{}, errors.New("the grant module's output is required")
	}
	response, err := api.Execute[generated.RegionGrantDescription](ctx, g.client.executor, "ConfirmRegionGrant", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return g.client.generated.ConfirmRegionGrant(ctx, g.client.accountID,
				generated.ConfirmRegionGrantJSONRequestBody{GrantId: grantID, Grant: grant})
		})
	if err != nil {
		return RegionGrant{}, err
	}
	return regionGrantFromResponse(response), nil
}

// WaitUntilConfirmed confirms the grant and describes it until it is READY. It stops when the grant
// reports CONFIRMATION_FAILED, which Restate does once it has tried to sign in for its check window,
// and when the context's deadline passes, which carries the cloud's last refusal. A later call while
// the grant is still CONFIRMING with the same output keeps waiting on the same check.
func (g *RegionGrants) WaitUntilConfirmed(ctx context.Context, grantID string, grant map[string]string) (RegionGrant, error) {
	var lastRefusal string
	accepted := false
	confirmed, err := api.PollUntil(ctx, g.client.poll,
		func(ctx context.Context) (RegionGrant, error) {
			if accepted {
				return g.Get(ctx, grantID)
			}
			confirmed, err := g.Confirm(ctx, grantID, grant)
			if err == nil {
				accepted = true
			}
			return confirmed, err
		},
		func(observed RegionGrant) (bool, error) {
			lastRefusal = observed.StatusMessage
			switch {
			case observed.Status == RegionGrantReady:
				return true, nil
			case observed.Status.Ended():
				return false, &api.Error{Service: api.ServiceCloud, Operation: "ConfirmRegionGrant", StatusCode: http.StatusGone,
					Message: fmt.Sprintf("region grant %s is %s", grantID, observed.Status)}
			case observed.StatusReason == GrantConfirmationFailed:
				return false, fmt.Errorf("region grant %s: your cloud kept refusing Restate's sign-in: %s",
					grantID, orUnknown(observed.StatusMessage))
			default:
				return false, nil
			}
		})
	if err != nil {
		if errors.Is(err, api.ErrPollTimeout) && lastRefusal != "" {
			return RegionGrant{}, fmt.Errorf("%w; Restate is still signing in, and your cloud's last answer was: %s", err, lastRefusal)
		}
		return RegionGrant{}, err
	}
	return confirmed, nil
}

// Delete deletes a region grant. A grant that is already gone is success. The API refuses (409)
// while a region still uses the grant.
//
// Retry safety is RetryServerErrors: deleting a deleted grant changes nothing.
func (g *RegionGrants) Delete(ctx context.Context, grantID string, _ struct{}) error {
	if grantID == "" {
		return errors.New("a region grant ID is required")
	}
	err := api.ExecuteNoContent(ctx, g.client.executor, "DeleteRegionGrant", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return g.client.generated.DeleteRegionGrant(ctx, g.client.accountID,
				generated.DeleteRegionGrantJSONRequestBody{GrantId: grantID})
		})
	if api.IsNotFound(err) || api.IsGone(err) {
		return nil
	}
	return err
}

func regionGrantFromResponse(response generated.RegionGrantDescription) RegionGrant {
	grant := RegionGrant{
		StatusReason:  string(derefGrantReason(response.StatusReason)),
		StatusMessage: derefString(response.StatusMessage),
		ID:            response.GrantId,
		Owner:         RegionGrantOwner(response.Owner),
		OwnerID:       response.OwnerId,
		DisplayName:   response.DisplayName,
		Cloud:         CloudProvider(response.Cloud),
		Status:        RegionGrantStatus(response.Status),
		RegionIDs:     append([]string{}, response.RegionIds...),
		CreatedAt:     response.CreatedAt,
		ConfirmedAt:   derefString(response.ConfirmedAt),
		ExpiresAt:     derefString(response.ExpiresAt),
	}
	switch {
	case response.Aws != nil:
		grant.Account.AWSAccountID = response.Aws.AccountId
	case response.Gcp != nil:
		grant.Account.GCPProjectID = response.Gcp.ProjectId
	case response.Azure != nil:
		// The API compares subscriptions without regard to case, and a UUID prints in lower case.
		grant.Account.AzureSubscriptionID = strings.ToLower(response.Azure.SubscriptionId.String())
	}
	if response.GrantInputs != nil {
		grant.Inputs = &RegionGrantInputs{
			RestateIssuer:  response.GrantInputs.RestateIssuer,
			GrantSubject:   response.GrantInputs.GrantSubject,
			TokenAudience:  response.GrantInputs.TokenAudience,
			ResourcePrefix: response.GrantInputs.ResourcePrefix,
		}
	}
	return grant
}

func derefGrantReason(reason *generated.RegionGrantStatusReason) generated.RegionGrantStatusReason {
	if reason == nil {
		return ""
	}
	return *reason
}
