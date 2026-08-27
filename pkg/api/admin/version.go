package admin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// The Admin API is versioned by an integer that a server advertises as a supported range. The
// pinned contract in this directory describes one point in that range, and the generated client
// speaks exactly the request and response shapes that document defines.
//
// The provider therefore declares the range it can speak and requires the server's advertised range
// to overlap it. The contract exposes no per-request version selector, so this is a compatibility
// gate rather than a negotiation that downgrades anything: if the ranges are disjoint, the shapes
// will not match and the provider says so up front instead of failing later on a decode.
const (
	// MinSupportedAdminAPIVersion is the oldest Admin API version whose surface the pinned contract
	// is compatible with. Restate 1.7.6 -- the version this contract was published from -- itself
	// accepts v2 and above, so a server advertising less than this predates the endpoints and field
	// shapes the generated client uses.
	MinSupportedAdminAPIVersion int32 = 2

	// MaxSupportedAdminAPIVersion is the newest Admin API version the pinned contract describes.
	// A server advertising only versions above this has moved past the snapshot, and the provider
	// cannot assume its shapes still apply.
	MaxSupportedAdminAPIVersion int32 = 4

	// PinnedContractAdminAPIVersion is the version the pinned contract actually documents, and the
	// only one the generated client is known to match field for field. A server whose maximum is
	// below this is inside the supported range but is not what the client was generated against, so
	// the caller is told rather than left to discover a missing field at apply time.
	PinnedContractAdminAPIVersion int32 = 4

	// PinnedContractServerVersion is the Restate version the contract was published from. It appears
	// in diagnostics so an operator can compare it against their own server.
	PinnedContractServerVersion = "1.7.6"
)

// ServerInfo reads GET /version.
func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error) {
	response, err := api.Execute[generated.VersionInformation](ctx, c.executor, "Version", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return c.generated.Version(ctx)
		})
	if err != nil {
		return ServerInfo{}, err
	}

	// A response that reports no version range cannot be checked against, and treating it as
	// compatible would defeat the point of the check.
	if response.Version == "" && response.MinAdminApiVersion == 0 && response.MaxAdminApiVersion == 0 {
		return ServerInfo{}, &api.Error{
			Service:   api.ServiceAdmin,
			Operation: "Version",
			Message:   "the server's version response reported neither a version nor an Admin API version range; this endpoint may not be a Restate Admin API",
		}
	}

	info := ServerInfo{
		Version:            response.Version,
		MinAdminAPIVersion: response.MinAdminApiVersion,
		MaxAdminAPIVersion: response.MaxAdminApiVersion,
		IngressEndpoint:    derefString(response.IngressEndpoint),
	}
	if response.Features != nil {
		info.Features = make(map[string]bool, len(response.Features))
		for name, enabled := range response.Features {
			info.Features[name] = enabled
		}
	}
	return info, nil
}

// Compatibility is the result of checking a server against the provider's supported range.
type Compatibility struct {
	// Server is the version information the check was based on.
	Server ServerInfo
	// Compatible reports whether the ranges overlap at all.
	Compatible bool
	// NegotiatedVersion is the highest version both sides support. It is zero when incompatible.
	NegotiatedVersion int32
	// Warning is a non-fatal concern: the ranges overlap, but not at the version the client was
	// generated against.
	Warning string
	// Problem explains an incompatibility.
	Problem string
}

// CheckCompatibility compares a server's advertised range against the provider's supported range.
//
// It returns a result rather than an error because the two outcomes need different treatment: a
// disjoint range is a configuration error, while an overlap below the pinned contract's version is
// a warning that should not stop an apply that may well work.
func CheckCompatibility(info ServerInfo) Compatibility {
	result := Compatibility{Server: info}

	// A server that advertises an inverted range is misreporting; refuse rather than guess which
	// bound to trust.
	if info.MinAdminAPIVersion > info.MaxAdminAPIVersion {
		result.Problem = fmt.Sprintf("the Restate server at this Admin API endpoint reported an invalid Admin API version range (minimum %d is greater than maximum %d)",
			info.MinAdminAPIVersion, info.MaxAdminAPIVersion)
		return result
	}

	if info.MaxAdminAPIVersion < MinSupportedAdminAPIVersion {
		result.Problem = fmt.Sprintf("Restate server %s supports Admin API versions %d to %d, but this provider requires at least version %d. Upgrade the Restate server, or use a provider release built against an older contract",
			describeVersion(info), info.MinAdminAPIVersion, info.MaxAdminAPIVersion, MinSupportedAdminAPIVersion)
		return result
	}
	if info.MinAdminAPIVersion > MaxSupportedAdminAPIVersion {
		result.Problem = fmt.Sprintf("Restate server %s requires Admin API version %d or newer, but this provider was built against version %d (Restate %s) and supports at most version %d. Upgrade the provider",
			describeVersion(info), info.MinAdminAPIVersion, PinnedContractAdminAPIVersion, PinnedContractServerVersion, MaxSupportedAdminAPIVersion)
		return result
	}

	result.Compatible = true
	result.NegotiatedVersion = min(info.MaxAdminAPIVersion, MaxSupportedAdminAPIVersion)

	if result.NegotiatedVersion < PinnedContractAdminAPIVersion {
		result.Warning = fmt.Sprintf("Restate server %s supports Admin API versions %d to %d. This provider's API client was generated against version %d (Restate %s), so the highest version both sides support is %d. "+
			"Requests should be accepted, but a field this provider expects may be absent; upgrade the Restate server if you see unexpected empty attributes",
			describeVersion(info), info.MinAdminAPIVersion, info.MaxAdminAPIVersion,
			PinnedContractAdminAPIVersion, PinnedContractServerVersion, result.NegotiatedVersion)
	}

	return result
}

// CheckServerCompatibility reads the server's version and checks it in one step.
func (c *Client) CheckServerCompatibility(ctx context.Context) (Compatibility, error) {
	info, err := c.ServerInfo(ctx)
	if err != nil {
		return Compatibility{}, err
	}
	return CheckCompatibility(info), nil
}

// Health reads GET /health as a bounded connectivity probe.
//
// It is deliberately not part of any resource's read path. A transiently unhealthy environment must
// not create plan drift on unrelated resources.
func (c *Client) Health(ctx context.Context) error {
	return api.ExecuteNoContent(ctx, c.executor, "Health", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return c.generated.Health(ctx)
		})
}

func describeVersion(info ServerInfo) string {
	if info.Version == "" {
		return "(version not reported)"
	}
	return info.Version
}
