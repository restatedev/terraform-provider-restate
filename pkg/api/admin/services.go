package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// Services is the endpoint adapter for discovered services.
type Services struct{ client *Client }

// Services returns the service adapter.
func (c *Client) Services() *Services { return &Services{client: c} }

// Services implement neither Creator nor Deleter: a service is created and destroyed by deployment
// discovery, and pretending Terraform could do either would misrepresent who owns it.
var (
	_ api.Getter[string, Service]                 = (*Services)(nil)
	_ api.Updater[string, ModifyService, Service] = (*Services)(nil)
	_ api.Lister[Service]                         = (*Services)(nil)
)

// Get reads one service and its effective configuration.
func (s *Services) Get(ctx context.Context, name string) (Service, error) {
	if name == "" {
		return Service{}, errors.New("a service name is required")
	}

	response, err := api.Execute[generated.ServiceMetadata](ctx, s.client.executor, "GetService", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.GetService(ctx, name)
		})
	if err != nil {
		return Service{}, err
	}
	return serviceFrom(response), nil
}

// List enumerates every discovered service.
func (s *Services) List(ctx context.Context) ([]Service, error) {
	response, err := api.Execute[generated.ListServicesResponse](ctx, s.client.executor, "ListServices", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.ListServices(ctx)
		})
	if err != nil {
		return nil, err
	}

	services := make([]Service, 0, len(response.Services))
	for _, service := range response.Services {
		services = append(services, serviceFrom(service))
	}
	return services, nil
}

// Update applies the subset of service configuration the contract's modify request accepts.
//
// An unset field is omitted rather than sent as JSON null. The contract does not document what a null
// means -- whether it restores the SDK-discovered value or a server default -- so this does not guess.
//
// Retry safety is RetryServerErrors: the request sets absolute values rather than applying a delta.
func (s *Services) Update(ctx context.Context, name string, input ModifyService) (Service, error) {
	if name == "" {
		return Service{}, errors.New("a service name is required")
	}
	if input.IsEmpty() {
		// Nothing to apply. Read instead of sending an empty modify request, whose meaning is undefined.
		return s.Get(ctx, name)
	}

	body := generated.ModifyServiceJSONRequestBody{
		Public:                      input.Public,
		IdempotencyRetention:        input.IdempotencyRetention,
		JournalRetention:            input.JournalRetention,
		WorkflowCompletionRetention: input.WorkflowCompletionRetention,
		InactivityTimeout:           input.InactivityTimeout,
		AbortTimeout:                input.AbortTimeout,
	}

	response, err := api.Execute[generated.ServiceMetadata](ctx, s.client.executor, "ModifyService", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.ModifyService(ctx, name, body)
		})
	if err != nil {
		return Service{}, err
	}
	return serviceFrom(response), nil
}

// GetHandler reads one handler of a service.
func (s *Services) GetHandler(ctx context.Context, service, handler string) (Handler, error) {
	if service == "" {
		return Handler{}, errors.New("a service name is required")
	}
	if handler == "" {
		return Handler{}, errors.New("a handler name is required")
	}

	response, err := api.Execute[generated.HandlerMetadata](ctx, s.client.executor, "GetServiceHandler", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.GetServiceHandler(ctx, service, handler)
		})
	if err != nil {
		return Handler{}, err
	}
	return handlerFrom(response), nil
}

// ListHandlers enumerates a service's handlers.
func (s *Services) ListHandlers(ctx context.Context, service string) ([]Handler, error) {
	if service == "" {
		return nil, errors.New("a service name is required")
	}

	response, err := api.Execute[generated.ListServiceHandlersResponse](ctx, s.client.executor, "ListServiceHandlers", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return s.client.generated.ListServiceHandlers(ctx, service)
		})
	if err != nil {
		return nil, err
	}

	handlers := make([]Handler, 0, len(response.Handlers))
	for _, handler := range response.Handlers {
		handlers = append(handlers, handlerFrom(handler))
	}
	return handlers, nil
}

func serviceFrom(response generated.ServiceMetadata) Service {
	service := Service{
		Name:                        response.Name,
		Type:                        ServiceType(response.Ty),
		Revision:                    response.Revision,
		DeploymentID:                response.DeploymentId,
		Documentation:               derefString(response.Documentation),
		Metadata:                    derefMap(response.Metadata),
		Info:                        noticesFrom(response.Info),
		Public:                      response.Public,
		IdempotencyRetention:        derefString(response.IdempotencyRetention),
		JournalRetention:            derefString(response.JournalRetention),
		WorkflowCompletionRetention: derefString(response.WorkflowCompletionRetention),
		InactivityTimeout:           derefString(response.InactivityTimeout),
		AbortTimeout:                derefString(response.AbortTimeout),
		EnableLazyState:             response.EnableLazyState,
		RetryPolicy:                 serviceRetryPolicyFrom(response.RetryPolicy),
	}
	for _, handler := range response.Handlers {
		service.Handlers = append(service.Handlers, handlerFrom(handler))
	}
	return service
}

func handlerFrom(response generated.HandlerMetadata) Handler {
	handler := Handler{
		Name:                 response.Name,
		Documentation:        derefString(response.Documentation),
		InputDescription:     response.InputDescription,
		OutputDescription:    response.OutputDescription,
		Metadata:             derefMap(response.Metadata),
		Info:                 noticesFrom(response.Info),
		Public:               response.Public,
		IdempotencyRetention: derefString(response.IdempotencyRetention),
		JournalRetention:     derefString(response.JournalRetention),
		InactivityTimeout:    derefString(response.InactivityTimeout),
		AbortTimeout:         derefString(response.AbortTimeout),
		EnableLazyState:      response.EnableLazyState,
		RetryPolicy:          handlerRetryPolicyFrom(response.RetryPolicy),
	}
	if response.Ty != nil {
		handler.Type = HandlerType(*response.Ty)
	}
	return handler
}

func serviceRetryPolicyFrom(policy *generated.ServiceRetryPolicyMetadata) *RetryPolicy {
	if policy == nil {
		return nil
	}
	return &RetryPolicy{
		InitialInterval:      derefString(policy.InitialInterval),
		MaxInterval:          derefString(policy.MaxInterval),
		MaxAttempts:          policy.MaxAttempts,
		ExponentiationFactor: derefFloat(policy.ExponentiationFactor),
		OnMaxAttempts:        derefOnMaxAttempts(policy.OnMaxAttempts),
	}
}

func handlerRetryPolicyFrom(policy *generated.HandlerRetryPolicyMetadata) *RetryPolicy {
	if policy == nil {
		return nil
	}
	return &RetryPolicy{
		InitialInterval:      derefString(policy.InitialInterval),
		MaxInterval:          derefString(policy.MaxInterval),
		MaxAttempts:          policy.MaxAttempts,
		ExponentiationFactor: derefFloat(policy.ExponentiationFactor),
		OnMaxAttempts:        derefOnMaxAttempts(policy.OnMaxAttempts),
	}
}

func derefFloat(f *float32) *float64 {
	if f == nil {
		return nil
	}
	value := float64(*f)
	return &value
}

func derefOnMaxAttempts(o *generated.OnMaxAttempts) string {
	if o == nil {
		return ""
	}
	return string(*o)
}
