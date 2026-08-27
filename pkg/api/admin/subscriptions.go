package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// Subscriptions is the endpoint adapter for Kafka subscriptions.
type Subscriptions struct{ client *Client }

// Subscriptions returns the subscription adapter.
func (c *Client) Subscriptions() *Subscriptions { return &Subscriptions{client: c} }

// Subscriptions deliberately implement no Updater: the pinned contract has create, get, list and
// delete, and no update endpoint at all. Any change to a subscription's source, sink or options must
// therefore replace it, and there is no method here that could pretend otherwise.
var (
	_ api.Creator[CreateSubscription, Subscription] = (*Subscriptions)(nil)
	_ api.Getter[string, Subscription]              = (*Subscriptions)(nil)
	_ api.Deleter[string, struct{}]                 = (*Subscriptions)(nil)
	_ api.Lister[Subscription]                      = (*Subscriptions)(nil)
)

// CreateSubscription is a request to create a subscription.
type CreateSubscription struct {
	// Source is a Kafka topic URI, documented as kafka://<cluster>/<topic>.
	Source string
	// Sink is a handler URI, documented as service://<service>/<handler>.
	Sink string
	// Options are subscription options. A nil map is omitted from the request.
	Options map[string]string
}

// URIWarning describes a source or sink URI that does not match the shape this contract snapshot
// documents. It is advisory: a newer server may accept a scheme this snapshot has never heard of,
// and rejecting it would make the provider the reason a supported configuration cannot be expressed.
type URIWarning struct {
	Field   string
	Value   string
	Message string
}

// ValidateSubscriptionURIs checks source and sink against the documented forms and returns advisory
// warnings. It returns an error only for a URI that cannot be parsed at all, which no server could
// accept.
func ValidateSubscriptionURIs(source, sink string) ([]URIWarning, error) {
	var warnings []URIWarning

	sourceWarning, err := checkURI("source", source, IsKnownSourceScheme, KnownSourceSchemes(), "kafka://<cluster>/<topic>")
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, sourceWarning...)

	sinkWarning, err := checkURI("sink", sink, IsKnownSinkScheme, KnownSinkSchemes(), "service://<service>/<handler>")
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, sinkWarning...)

	return warnings, nil
}

func checkURI(field, raw string, known func(string) bool, knownSchemes []string, documented string) ([]URIWarning, error) {
	if raw == "" {
		return nil, fmt.Errorf("a subscription %s is required", field)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("subscription %s %q is not a valid URI: %w", field, raw, err)
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("subscription %s %q has no scheme; the documented form is %s", field, raw, documented)
	}

	if !known(parsed.Scheme) {
		return []URIWarning{{
			Field: field, Value: raw,
			Message: fmt.Sprintf("subscription %s uses scheme %q, which this provider's pinned Admin API contract does not document (it documents %v). "+
				"The request is being sent unchanged: a newer Restate server may support it. If the server rejects it, check the scheme",
				field, parsed.Scheme, knownSchemes),
		}}, nil
	}

	// A documented scheme with no host or no path cannot address anything, whatever the server
	// version, but a server-side rejection is still the authority, so this is a warning too.
	if parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return []URIWarning{{
			Field: field, Value: raw,
			Message: fmt.Sprintf("subscription %s %q does not match the documented form %s and is likely to be rejected", field, raw, documented),
		}}, nil
	}

	return nil, nil
}

// Create creates a subscription.
//
// Retry safety is RetryThrottleOnly: the API assigns the identifier, so a retry after an ambiguous
// 5xx could create a second subscription that nothing tracks.
func (s *Subscriptions) Create(ctx context.Context, input CreateSubscription) (Subscription, error) {
	if _, err := ValidateSubscriptionURIs(input.Source, input.Sink); err != nil {
		return Subscription{}, err
	}

	body := generated.CreateSubscriptionJSONRequestBody{
		Source:  input.Source,
		Sink:    input.Sink,
		Options: mapPointer(input.Options),
	}

	response, err := api.Execute[generated.SubscriptionResponse](ctx, s.client.executor, "CreateSubscription", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.CreateSubscription(ctx, body)
		})
	if err != nil {
		return Subscription{}, err
	}
	if response.Id == "" {
		return Subscription{}, &api.Error{
			Service:   api.ServiceAdmin,
			Operation: "CreateSubscription",
			Message:   "the API reported success but returned no subscription ID",
		}
	}
	return subscriptionFrom(response), nil
}

// Get reads one subscription.
func (s *Subscriptions) Get(ctx context.Context, subscriptionID string) (Subscription, error) {
	if subscriptionID == "" {
		return Subscription{}, errors.New("a subscription ID is required")
	}

	response, err := api.Execute[generated.SubscriptionResponse](ctx, s.client.executor, "GetSubscription", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.GetSubscription(ctx, subscriptionID)
		})
	if err != nil {
		return Subscription{}, err
	}
	return subscriptionFrom(response), nil
}

// List enumerates every subscription.
func (s *Subscriptions) List(ctx context.Context) ([]Subscription, error) {
	return s.list(ctx, nil)
}

// ListFiltered enumerates subscriptions matching a source and/or sink. An empty filter value is
// omitted rather than sent as an empty query parameter.
func (s *Subscriptions) ListFiltered(ctx context.Context, source, sink string) ([]Subscription, error) {
	params := &generated.ListSubscriptionsParams{
		Source: stringPointer(source),
		Sink:   stringPointer(sink),
	}
	return s.list(ctx, params)
}

func (s *Subscriptions) list(ctx context.Context, params *generated.ListSubscriptionsParams) ([]Subscription, error) {
	if params == nil {
		params = &generated.ListSubscriptionsParams{}
	}

	response, err := api.Execute[generated.ListSubscriptionsResponse](ctx, s.client.executor, "ListSubscriptions", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.ListSubscriptions(ctx, params)
		})
	if err != nil {
		return nil, err
	}

	subscriptions := make([]Subscription, 0, len(response.Subscriptions))
	for _, subscription := range response.Subscriptions {
		subscriptions = append(subscriptions, subscriptionFrom(subscription))
	}
	return subscriptions, nil
}

// Delete removes a subscription. It is idempotent: an already-absent subscription is success.
func (s *Subscriptions) Delete(ctx context.Context, subscriptionID string, _ struct{}) error {
	if subscriptionID == "" {
		return errors.New("a subscription ID is required")
	}

	err := api.ExecuteNoContent(ctx, s.client.executor, "DeleteSubscription", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.DeleteSubscription(ctx, subscriptionID)
		})
	if api.IsNotFound(err) {
		return nil
	}
	return err
}

func subscriptionFrom(response generated.SubscriptionResponse) Subscription {
	subscription := Subscription{
		ID:     response.Id,
		Source: response.Source,
		Sink:   response.Sink,
	}
	if response.Options != nil {
		subscription.Options = make(map[string]string, len(response.Options))
		for name, value := range response.Options {
			subscription.Options[name] = value
		}
	}
	return subscription
}
