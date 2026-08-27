package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// Environments is the endpoint adapter for Cloud environments.
type Environments struct{ client *Client }

// Environments returns the environments adapter for this client's account.
func (c *Client) Environments() *Environments { return &Environments{client: c} }

// Environments supports create, read, list and delete. It deliberately implements no generic
// Updater: the contract splits environment mutation across two operations with materially
// different semantics — UpdateEnvironment renames and always succeeds non-interactively, while
// UpdateEnvironmentTier changes billing and may demand interactive payment. Hiding that behind one
// symmetrical Update would make the risky half invisible at the call site.
var (
	_ api.Creator[CreateEnvironmentInput, CreatedEnvironment] = (*Environments)(nil)
	_ api.Getter[string, Environment]                         = (*Environments)(nil)
	_ api.Lister[EnvironmentSummary]                          = (*Environments)(nil)
	_ api.Deleter[string, DestroyOptions]                     = (*Environments)(nil)
)

// DestroyOptions has no fields today: DestroyEnvironment takes only an environment ID, and the
// contract offers no force or cascade switch. It exists so the Deleter capability is satisfied
// without a bare struct{} at every call site, and so a future destructive switch has an obvious,
// opt-in home rather than being bolted onto the method signature.
type DestroyOptions struct{}

// ErrInteractiveCheckoutRequired reports that an operation cannot complete without a human
// completing a browser payment flow. Terraform cannot do that, so the provider refuses rather than
// reporting success over a half-applied change.
//
// It carries the environment ID because the environment may already exist in a partially
// provisioned state, and an operator needs the identifier to find and clean it up.
type ErrInteractiveCheckoutRequired struct {
	Operation     string
	EnvironmentID string
}

func (e *ErrInteractiveCheckoutRequired) Error() string {
	subject := "this operation"
	if e.EnvironmentID != "" {
		subject = fmt.Sprintf("environment %s", e.EnvironmentID)
	}
	return fmt.Sprintf("%s requires interactive payment to complete for %s: the Cloud Management API returned a Stripe checkout URL, which Terraform cannot complete. "+
		"Complete the change in the Restate Cloud console, or switch the account to account billing, and then re-run Terraform",
		e.Operation, subject)
}

// IsInteractiveCheckoutRequired reports whether err is a refused interactive payment flow.
func IsInteractiveCheckoutRequired(err error) bool {
	var target *ErrInteractiveCheckoutRequired
	return errors.As(err, &target)
}

// Create provisions an environment and returns the API's acknowledgement.
//
// The returned value is not proof that provisioning completed. The caller must poll — see
// WaitUntilActive — because the create response reports an identifier and a name and nothing about
// readiness.
//
// Retry safety is RetryThrottleOnly: a 5xx from a create may mean the environment exists, and
// retrying could leave an orphan behind that Terraform does not know about.
func (e *Environments) Create(ctx context.Context, input CreateEnvironmentInput) (CreatedEnvironment, error) {
	if input.Name == "" {
		return CreatedEnvironment{}, errors.New("an environment name is required")
	}
	if input.Tier != "" && !input.Tier.Requestable() {
		return CreatedEnvironment{}, fmt.Errorf("tier %q cannot be requested; expected one of %v", input.Tier, RequestableTiers())
	}

	body := generated.CreateEnvironmentJSONRequestBody{Name: input.Name}
	if input.Region != "" {
		body.Region = &input.Region
	}
	if input.Tier != "" {
		tier := generated.CreateEnvironmentRequestTierId(input.Tier)
		body.TierId = &tier
	}
	// Payment is deliberately never populated. Supplying success and cancel URLs is how a caller
	// opts into a browser checkout flow, which Terraform cannot complete.

	response, err := api.Execute[generated.CreateEnvironmentResponse](ctx, e.client.executor, "CreateEnvironment", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.CreateEnvironment(ctx, e.client.accountID, body)
		})
	if err != nil {
		return CreatedEnvironment{}, err
	}

	// A checkout URL means the tier was not applied and the account owes a payment. Report the
	// environment ID regardless, because the environment may already have been created.
	if response.CheckoutUrl != nil && *response.CheckoutUrl != "" {
		return CreatedEnvironment{ID: response.EnvironmentId, Name: response.Name},
			&ErrInteractiveCheckoutRequired{Operation: "CreateEnvironment", EnvironmentID: response.EnvironmentId}
	}
	if response.EnvironmentId == "" {
		return CreatedEnvironment{}, &api.Error{
			Service:   api.ServiceCloud,
			Operation: "CreateEnvironment",
			Message:   "the API reported success but returned no environment ID",
		}
	}

	return CreatedEnvironment{ID: response.EnvironmentId, Name: response.Name}, nil
}

// Get describes one environment.
//
// A deleted environment is reported either as a not-found error or as an environment whose status
// is DELETED, depending on how long ago it was destroyed. Callers must treat both as absence; see
// GetExisting.
func (e *Environments) Get(ctx context.Context, environmentID string) (Environment, error) {
	if environmentID == "" {
		return Environment{}, errors.New("an environment ID is required")
	}

	response, err := api.Execute[generated.DescribeEnvironmentResponse](ctx, e.client.executor, "DescribeEnvironment", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.DescribeEnvironment(ctx, e.client.accountID,
				generated.DescribeEnvironmentJSONRequestBody{EnvironmentId: environmentID})
		})
	if err != nil {
		return Environment{}, err
	}

	return environmentFromResponse(response), nil
}

// ErrEnvironmentGone reports that an environment no longer exists, whether because the API returned
// not-found or because it reported the environment as DELETED. Callers remove such a resource from
// state rather than trying to converge it.
var ErrEnvironmentGone = errors.New("the environment no longer exists")

// GetExisting describes an environment and collapses both forms of absence into
// ErrEnvironmentGone, so a refresh has a single condition to check.
func (e *Environments) GetExisting(ctx context.Context, environmentID string) (Environment, error) {
	environment, err := e.Get(ctx, environmentID)
	if err != nil {
		if api.IsNotFound(err) {
			return Environment{}, fmt.Errorf("%w: %s", ErrEnvironmentGone, environmentID)
		}
		return Environment{}, err
	}
	if environment.Status == StatusDeleted {
		return environment, fmt.Errorf("%w: %s reports status %s", ErrEnvironmentGone, environmentID, environment.Status)
	}
	return environment, nil
}

// List enumerates the account's environments. The contract's list response carries no status and no
// connection metadata, so callers that need those must describe each environment.
func (e *Environments) List(ctx context.Context) ([]EnvironmentSummary, error) {
	response, err := api.Execute[generated.ListEnvironmentsResponse](ctx, e.client.executor, "ListEnvironments", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.ListEnvironments(ctx, e.client.accountID, generated.ListEnvironmentsJSONRequestBody{})
		})
	if err != nil {
		return nil, err
	}

	summaries := make([]EnvironmentSummary, 0, len(response.Environments))
	for _, environment := range response.Environments {
		summaries = append(summaries, EnvironmentSummary{
			ID:     environment.EnvironmentId,
			Name:   environment.Name,
			Region: environment.Region,
			Tier:   Tier(environment.TierId),
		})
	}
	return summaries, nil
}

// Rename changes an environment's name.
//
// Retry safety is RetryServerErrors: setting a name to a value it may already hold is harmless, so
// repeating the request cannot do damage.
func (e *Environments) Rename(ctx context.Context, environmentID, name string) error {
	if environmentID == "" {
		return errors.New("an environment ID is required")
	}
	if name == "" {
		return errors.New("an environment name is required")
	}

	response, err := api.Execute[generated.UpdateEnvironmentResponse](ctx, e.client.executor, "UpdateEnvironment", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.UpdateEnvironment(ctx, e.client.accountID,
				generated.UpdateEnvironmentJSONRequestBody{EnvironmentId: environmentID, Name: name})
		})
	if err != nil {
		return err
	}

	// The API echoes the applied name. Disagreement means the request was accepted but not applied
	// as asked, which would otherwise surface as unexplained drift on the next plan.
	if response.Name != "" && response.Name != name {
		return &api.Error{
			Service:   api.ServiceCloud,
			Operation: "UpdateEnvironment",
			Message:   fmt.Sprintf("requested name %q but the API applied %q", name, response.Name),
		}
	}
	return nil
}

// SetTier changes an environment's tier.
//
// A stripe-billed account may be redirected to a browser checkout, which is reported as
// ErrInteractiveCheckoutRequired rather than treated as success. Callers should check the account's
// billing mode first so a plan can warn before an apply discovers this.
//
// Retry safety is RetryServerErrors: the operation sets a target tier rather than applying a delta,
// so repeating it is idempotent.
func (e *Environments) SetTier(ctx context.Context, environmentID string, tier Tier) error {
	if environmentID == "" {
		return errors.New("an environment ID is required")
	}
	if !tier.Requestable() {
		return fmt.Errorf("tier %q cannot be requested; expected one of %v", tier, RequestableTiers())
	}

	response, err := api.Execute[generated.UpdateEnvironmentTierResponse](ctx, e.client.executor, "UpdateEnvironmentTier", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.UpdateEnvironmentTier(ctx, e.client.accountID,
				generated.UpdateEnvironmentTierJSONRequestBody{
					EnvironmentId: environmentID,
					TierId:        generated.UpdateEnvironmentTierRequestTierId(tier),
				})
		})
	if err != nil {
		return err
	}

	if response.CheckoutUrl != nil && *response.CheckoutUrl != "" {
		return &ErrInteractiveCheckoutRequired{Operation: "UpdateEnvironmentTier", EnvironmentID: environmentID}
	}
	return nil
}

// Delete destroys an environment. It is idempotent: an already-absent environment is success, which
// is what makes a re-run of a partially failed destroy converge.
//
// Delete returns as soon as the API accepts the request. Callers that need the environment to be
// gone must follow with WaitUntilDeleted.
func (e *Environments) Delete(ctx context.Context, environmentID string, _ DestroyOptions) error {
	if environmentID == "" {
		return errors.New("an environment ID is required")
	}

	err := api.ExecuteNoContent(ctx, e.client.executor, "DestroyEnvironment", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return e.client.generated.DestroyEnvironment(ctx, e.client.accountID,
				generated.DestroyEnvironmentJSONRequestBody{EnvironmentId: environmentID})
		})
	if api.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitUntilActive polls until the environment reaches a terminal status, and reports an error
// unless that status is ACTIVE.
//
// A FAILED environment fails immediately rather than at the timeout, and the error names the
// environment ID so an operator can inspect and clean up the failed provisioning attempt.
func (e *Environments) WaitUntilActive(ctx context.Context, environmentID string) (Environment, error) {
	return api.PollUntil(ctx, e.client.poll,
		func(ctx context.Context) (Environment, error) {
			environment, err := e.Get(ctx, environmentID)
			if err != nil {
				// A newly created environment can briefly not be visible to describe. Treat that as
				// "not ready yet" rather than as a failure, but keep every other error fatal so an
				// expired credential is not reported as a timeout.
				if api.IsNotFound(err) {
					return Environment{ID: environmentID, Status: StatusPending}, nil
				}
				return Environment{}, err
			}
			return environment, nil
		},
		func(environment Environment) (bool, error) {
			switch environment.Status {
			case StatusActive:
				return true, nil
			case StatusFailed:
				return false, fmt.Errorf("environment %s failed to provision; it still exists and must be inspected or destroyed manually", environmentID)
			case StatusDeleted:
				return false, fmt.Errorf("environment %s was deleted while waiting for it to become active", environmentID)
			default:
				return false, nil
			}
		})
}

// WaitUntilDeleted polls until the environment is gone, treating both a not-found response and a
// DELETED status as success.
func (e *Environments) WaitUntilDeleted(ctx context.Context, environmentID string) error {
	_, err := api.PollUntil(ctx, e.client.poll,
		func(ctx context.Context) (EnvironmentStatus, error) {
			environment, err := e.Get(ctx, environmentID)
			if err != nil {
				if api.IsNotFound(err) {
					return StatusDeleted, nil
				}
				return "", err
			}
			return environment.Status, nil
		},
		func(status EnvironmentStatus) (bool, error) {
			if status == StatusDeleted {
				return true, nil
			}
			if status == StatusFailed {
				// A destroy that ends in FAILED is not converging on its own; waiting out the
				// timeout would only delay the diagnostic.
				return false, fmt.Errorf("environment %s reports status FAILED while being destroyed; it must be inspected manually", environmentID)
			}
			return false, nil
		})
	return err
}

func environmentFromResponse(response generated.DescribeEnvironmentResponse) Environment {
	keyIDs := make([]string, 0, len(response.ApiKeys))
	for _, key := range response.ApiKeys {
		keyIDs = append(keyIDs, key.KeyId)
	}

	return Environment{
		ID:                   response.EnvironmentId,
		Name:                 response.Name,
		Region:               response.Region,
		RegionDisplayName:    derefString(response.RegionDisplayName),
		Status:               EnvironmentStatus(response.Status),
		Tier:                 Tier(response.TierId),
		SigningPublicKey:     response.SigningPublicKey,
		IngressBaseURL:       response.IngressBaseUrl,
		AdminBaseURL:         response.AdminBaseUrl,
		InvocationMetricsURL: derefString(response.InvocationMetricsUrl),
		TunnelBaseURL:        response.TunnelBaseUrl,
		TunnelSRV:            response.TunnelSrv,
		ProxyBaseURL:         response.ProxyBaseUrl,
		APIKeyIDs:            keyIDs,
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
