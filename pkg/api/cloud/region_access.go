package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// SetAccess replaces the whole access setting of an AWS region built with a grant, and answers the
// setting the API stored. A field left out of access returns to its default, so the caller sends the
// full setting every time. The endpoint service is not part of the answer; Get describes it once AWS
// has created it.
//
// Retry safety is RetryServerErrors: the call sets an absolute value, so repeating it changes nothing.
func (r *ByocRegions) SetAccess(ctx context.Context, regionID string, access RegionAccess) (RegionAccess, error) {
	if regionID == "" {
		return RegionAccess{}, errors.New("a region ID is required")
	}

	body := generated.SetRegionAccessJSONRequestBody{RegionId: regionID}
	if access.EntrySourceCIDRs != nil {
		cidrs := append([]string{}, access.EntrySourceCIDRs...)
		body.EntrySourceCidrs = &cidrs
	}
	if privateLink := access.PrivateLink; privateLink != nil {
		acceptanceRequired := privateLink.AcceptanceRequired
		wire := &struct {
			AcceptanceRequired *bool     `json:"acceptanceRequired,omitempty"`
			AllowedPrincipals  *[]string `json:"allowedPrincipals,omitempty"`
			Enabled            bool      `json:"enabled"`
		}{AcceptanceRequired: &acceptanceRequired, Enabled: privateLink.Enabled}
		if len(privateLink.AllowedPrincipals) > 0 {
			principals := append([]string{}, privateLink.AllowedPrincipals...)
			wire.AllowedPrincipals = &principals
		}
		body.PrivateLink = wire
	}

	response, err := api.Execute[generated.SetRegionAccessResponse](ctx, r.client.executor, "SetRegionAccess", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return r.client.generated.SetRegionAccess(ctx, r.client.accountID, body)
		})
	if err != nil {
		return RegionAccess{}, err
	}

	stored := RegionAccess{EntrySourceCIDRs: copyOptionalStrings(response.EntrySourceCidrs)}
	if privateLink := response.PrivateLink; privateLink != nil {
		stored.PrivateLink = &RegionPrivateLink{
			Enabled:            privateLink.Enabled,
			AllowedPrincipals:  copyOptionalStrings(privateLink.AllowedPrincipals),
			AcceptanceRequired: derefBool(privateLink.AcceptanceRequired),
		}
	}
	return stored, nil
}

// SetAccessWhenReady is SetAccess that keeps trying while the answer says "not yet": a 503, because
// the region's infrastructure cannot take the setting yet, and a 409 for a region that is still being
// built. Every other refusal, including a 409 for a region that is built, ends it at once. The
// context's deadline bounds it, and the error then carries the last answer.
func (r *ByocRegions) SetAccessWhenReady(ctx context.Context, regionID string, access RegionAccess) (RegionAccess, error) {
	return r.setAccessWhenReady(ctx, regionID, access, false)
}

// ResetAccess returns a region's access to its defaults: every address admitted and no endpoint
// service. A region that is gone, or being deleted, has nothing left to reset and is success.
func (r *ByocRegions) ResetAccess(ctx context.Context, regionID string) error {
	_, err := r.setAccessWhenReady(ctx, regionID, RegionAccess{}, true)
	return err
}

type accessAttempt struct {
	stored  RegionAccess
	done    bool
	pending error
}

func (r *ByocRegions) setAccessWhenReady(ctx context.Context, regionID string, access RegionAccess, reset bool) (RegionAccess, error) {
	var lastPending error
	attempt, err := api.PollUntil(ctx, r.client.poll,
		func(ctx context.Context) (accessAttempt, error) {
			stored, err := r.SetAccess(ctx, regionID, access)
			switch {
			case err == nil:
				return accessAttempt{stored: stored, done: true}, nil
			case api.IsServiceUnavailable(err):
				return accessAttempt{pending: err}, nil
			case reset && api.IsNotFound(err):
				return accessAttempt{done: true}, nil
			case api.IsConflict(err):
				region, describeErr := r.Get(ctx, regionID)
				if reset && api.IsNotFound(describeErr) {
					return accessAttempt{done: true}, nil
				}
				if describeErr != nil {
					return accessAttempt{}, errors.Join(err, describeErr)
				}
				switch {
				case region.Status == RegionPending:
					return accessAttempt{pending: err}, nil
				case reset && (region.Status == RegionDeleting || region.Status == RegionDeleted):
					return accessAttempt{done: true}, nil
				}
				return accessAttempt{}, err
			default:
				return accessAttempt{}, err
			}
		},
		func(attempt accessAttempt) (bool, error) {
			lastPending = attempt.pending
			return attempt.done, nil
		})
	if err != nil {
		if errors.Is(err, api.ErrPollTimeout) && lastPending != nil {
			return RegionAccess{}, fmt.Errorf("%w; the last answer was: %w", err, lastPending)
		}
		return RegionAccess{}, err
	}
	return attempt.stored, nil
}

// WaitForEndpointService describes the region until AWS has created the endpoint service of an
// enabled PrivateLink and reports its name. It stops with an error when the region is gone, or when
// PrivateLink is no longer enabled, since no service will appear then.
func (r *ByocRegions) WaitForEndpointService(ctx context.Context, regionID string) (ByocRegion, error) {
	return api.PollUntil(ctx, r.client.poll,
		func(ctx context.Context) (ByocRegion, error) { return r.GetExisting(ctx, regionID) },
		func(region ByocRegion) (bool, error) {
			privateLink := (*RegionPrivateLink)(nil)
			if region.Access != nil {
				privateLink = region.Access.PrivateLink
			}
			switch {
			case privateLink == nil || !privateLink.Enabled:
				return false, fmt.Errorf("region %s has no enabled PrivateLink, so AWS creates no endpoint service for it", regionID)
			case privateLink.EndpointService != nil && privateLink.EndpointService.ServiceName != "":
				return true, nil
			default:
				return false, nil
			}
		})
}

func regionAccessFromDescription(response generated.RegionDescription) *RegionAccess {
	if response.Access == nil {
		return nil
	}
	access := &RegionAccess{EntrySourceCIDRs: copyOptionalStrings(response.Access.EntrySourceCidrs)}
	if privateLink := response.Access.PrivateLink; privateLink != nil {
		access.PrivateLink = &RegionPrivateLink{
			Enabled:            privateLink.Enabled,
			AllowedPrincipals:  copyOptionalStrings(privateLink.AllowedPrincipals),
			AcceptanceRequired: derefBool(privateLink.AcceptanceRequired),
		}
		if service := privateLink.EndpointService; service != nil {
			access.PrivateLink.EndpointService = &RegionEndpointService{
				ServiceName:         derefString(service.ServiceName),
				State:               derefString(service.State),
				AvailabilityZoneIDs: copyOptionalStrings(service.AvailabilityZoneIds),
			}
		}
	}
	return access
}

// copyOptionalStrings keeps an absent list nil and an empty one non-nil, which is the difference
// between "every address" and "no address" for entry source CIDRs.
func copyOptionalStrings(list *[]string) []string {
	if list == nil {
		return nil
	}
	return append([]string{}, *list...)
}

func derefBool(value *bool) bool { return value != nil && *value }
