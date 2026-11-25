package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// ByocRegions is the endpoint adapter for BYOC regions built with a region grant.
//
// Regions set up with a grant of their own (SetRegionGrant) are not covered: the grant module in
// modules/byoc creates those together with their grant.
type ByocRegions struct{ client *Client }

// ByocRegions returns the BYOC regions adapter for this client's account.
func (c *Client) ByocRegions() *ByocRegions { return &ByocRegions{client: c} }

// No Deleter: one DeleteRegion call cannot finish a teardown, so the only deletion offered is
// DeleteAndWait.
var (
	_ api.Creator[CreateRegionInput, ByocRegion] = (*ByocRegions)(nil)
	_ api.Getter[string, ByocRegion]             = (*ByocRegions)(nil)
)

// ErrRegionGone reports that a BYOC region no longer exists, by a 404 or by status DELETED.
var ErrRegionGone = errors.New("the BYOC region no longer exists")

// Create starts building a region with a READY region grant. The region's cloud and cloud account
// must be the grant's. It returns once the API has accepted the region; WaitUntilActive follows.
//
// Retry safety is RetryServerErrors: the name is the region's key within the account, and a repeat
// with the same settings answers the same region.
func (r *ByocRegions) Create(ctx context.Context, input CreateRegionInput) (ByocRegion, error) {
	if input.Name == "" {
		return ByocRegion{}, errors.New("a region name is required")
	}
	if input.GrantID == "" {
		return ByocRegion{}, errors.New("a region grant ID is required")
	}

	setup := generated.RegionSetup("GRANT")
	body := generated.CreateRegionJSONRequestBody{
		Name:        input.Name,
		Setup:       &setup,
		Cloud:       generated.CreateRegionRequestCloud(input.Cloud),
		CloudRegion: input.CloudRegion,
		GrantId:     &input.GrantID,
	}
	if input.DisplayName != "" {
		body.DisplayName = &input.DisplayName
	}
	if input.MachineType != "" {
		body.Infrastructure = &struct {
			MachineType *string `json:"machineType,omitempty"`
		}{MachineType: &input.MachineType}
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
		body.Azure = &struct {
			SubscriptionId string `json:"subscriptionId"`
		}{SubscriptionId: input.Account.AzureSubscriptionID}
	default:
		return ByocRegion{}, fmt.Errorf("unknown cloud %q", input.Cloud)
	}

	response, err := api.Execute[generated.CreateRegionResponse](ctx, r.client.executor, "CreateRegion", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.CreateRegion(ctx, r.client.accountID, body)
		})
	if err != nil {
		return ByocRegion{}, err
	}
	if response.RegionId == "" {
		return ByocRegion{}, &api.Error{Service: api.ServiceCloud, Operation: "CreateRegion",
			Message: "the API reported success but returned no region ID"}
	}
	return byocRegionFromResponse(response), nil
}

// Get describes one region by its ID, whatever its status.
func (r *ByocRegions) Get(ctx context.Context, regionID string) (ByocRegion, error) {
	if regionID == "" {
		return ByocRegion{}, errors.New("a region ID is required")
	}
	response, err := api.Execute[generated.DescribeRegionResponse](ctx, r.client.executor, "DescribeRegion", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.DescribeRegion(ctx, r.client.accountID,
				generated.DescribeRegionJSONRequestBody{RegionId: &regionID})
		})
	if err != nil {
		return ByocRegion{}, err
	}
	return byocRegionFromResponse(response), nil
}

// GetExisting collapses a 404 and status DELETED into ErrRegionGone.
func (r *ByocRegions) GetExisting(ctx context.Context, regionID string) (ByocRegion, error) {
	region, err := r.Get(ctx, regionID)
	if err != nil {
		if api.IsNotFound(err) {
			return ByocRegion{}, fmt.Errorf("%w: %s", ErrRegionGone, regionID)
		}
		return ByocRegion{}, err
	}
	if region.Status == RegionDeleted {
		return region, fmt.Errorf("%w: %s is %s", ErrRegionGone, regionID, region.Status)
	}
	return region, nil
}

// DeleteAndWait asks for the region's deletion until the region is gone. The API answers 409 both
// while the teardown runs and when the region cannot be deleted, for example because it still has
// environments, so each 409 is resolved by describing the region. The context's deadline bounds it.
//
// Retry safety is RetryServerErrors: a repeat while the teardown runs starts nothing new.
func (r *ByocRegions) DeleteAndWait(ctx context.Context, regionID string) error {
	_, err := api.PollUntil(ctx, r.client.poll,
		func(ctx context.Context) (bool, error) { return r.deleteOnce(ctx, regionID) },
		func(done bool) (bool, error) { return done, nil })
	return err
}

func (r *ByocRegions) deleteOnce(ctx context.Context, regionID string) (bool, error) {
	if regionID == "" {
		return false, errors.New("a region ID is required")
	}
	response, err := api.Execute[generated.DeleteRegionResponse](ctx, r.client.executor, "DeleteRegion", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.DeleteRegion(ctx, r.client.accountID,
				generated.DeleteRegionJSONRequestBody{RegionId: &regionID})
		})
	switch {
	case err == nil:
		return RegionStatus(response.Status) == RegionDeleted, nil
	case api.IsNotFound(err):
		return true, nil
	case api.IsConflict(err):
		region, describeErr := r.Get(ctx, regionID)
		if describeErr != nil {
			if api.IsNotFound(describeErr) {
				return true, nil
			}
			return false, errors.Join(err, describeErr)
		}
		switch region.Status {
		case RegionDeleting:
			return false, nil
		case RegionDeleted:
			return true, nil
		default:
			return false, err
		}
	default:
		return false, err
	}
}

// WaitUntilActive polls until the region is ACTIVE. FAILED, DELETING and DELETED end the wait with an
// error that names the region, so it can be found and inspected.
func (r *ByocRegions) WaitUntilActive(ctx context.Context, regionID string) (ByocRegion, error) {
	return api.PollUntil(ctx, r.client.poll,
		func(ctx context.Context) (ByocRegion, error) { return r.Get(ctx, regionID) },
		func(region ByocRegion) (bool, error) {
			switch region.Status {
			case RegionActive:
				return true, nil
			case RegionFailed:
				return false, fmt.Errorf("region %s failed to build (%s); it still exists and must be inspected or deleted",
					regionID, orUnknown(region.StatusReason))
			case RegionDeleting, RegionDeleted:
				return false, fmt.Errorf("region %s is %s; it was deleted while it was being built", regionID, region.Status)
			default:
				return false, nil
			}
		})
}

func byocRegionFromResponse(response generated.RegionDescription) ByocRegion {
	region := ByocRegion{
		ID:           response.RegionId,
		Name:         response.Name,
		DisplayName:  response.DisplayName,
		Cloud:        CloudProvider(response.Cloud),
		CloudRegion:  response.CloudRegion,
		MachineType:  derefString(response.Infrastructure.MachineType),
		GrantID:      derefString(response.GrantId),
		Status:       RegionStatus(response.Status),
		StatusReason: derefString(response.StatusReason),
		CreatedAt:    response.CreatedAt,
		Access:       regionAccessFromDescription(response),
	}
	switch {
	case response.Aws != nil:
		region.Account.AWSAccountID = response.Aws.AccountId
	case response.Gcp != nil:
		region.Account.GCPProjectID = response.Gcp.ProjectId
	case response.Azure != nil:
		region.Account.AzureSubscriptionID = response.Azure.SubscriptionId
	}
	return region
}

func orUnknown(reason string) string {
	if reason == "" {
		return "no reason reported"
	}
	return reason
}
