package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// Deployments is the endpoint adapter for service deployments.
type Deployments struct{ client *Client }

// Deployments returns the deployment adapter.
func (c *Client) Deployments() *Deployments { return &Deployments{client: c} }

// Deployments support the full set of capabilities. RegisterInput and UpdateInput are distinct
// types, so a create request cannot be reused as an update request -- which matters here, because
// the contract's update request accepts strictly fewer fields than its register request.
var (
	_ api.Creator[RegisterInput, RegisteredDeployment] = (*Deployments)(nil)
	_ api.Getter[string, Deployment]                   = (*Deployments)(nil)
	_ api.Updater[string, UpdateInput, Deployment]     = (*Deployments)(nil)
	_ api.Deleter[string, DeleteDeploymentOptions]     = (*Deployments)(nil)
	_ api.Lister[Deployment]                           = (*Deployments)(nil)
)

// RegisterInput is a deployment registration. Exactly one of HTTP and Lambda must be set: the
// contract models the two as an untagged union, and sending fields from both would produce a body
// that matches neither member.
type RegisterInput struct {
	HTTP   *RegisterHTTPDeployment
	Lambda *RegisterLambdaDeployment
}

// Kind reports which endpoint kind the input describes.
func (i RegisterInput) Kind() (EndpointKind, error) {
	switch {
	case i.HTTP != nil && i.Lambda != nil:
		return "", errors.New("a deployment registration must describe either an HTTP endpoint or a Lambda endpoint, not both")
	case i.HTTP != nil:
		return EndpointHTTP, nil
	case i.Lambda != nil:
		return EndpointLambda, nil
	default:
		return "", errors.New("a deployment registration must describe either an HTTP endpoint or a Lambda endpoint")
	}
}

// UpdateInput is a deployment update. As with RegisterInput, exactly one variant must be set.
type UpdateInput struct {
	HTTP   *UpdateHTTPDeployment
	Lambda *UpdateLambdaDeployment
}

// Kind reports which endpoint kind the input describes.
func (i UpdateInput) Kind() (EndpointKind, error) {
	switch {
	case i.HTTP != nil && i.Lambda != nil:
		return "", errors.New("a deployment update must describe either an HTTP endpoint or a Lambda endpoint, not both")
	case i.HTTP != nil:
		return EndpointHTTP, nil
	case i.Lambda != nil:
		return EndpointLambda, nil
	default:
		return "", errors.New("a deployment update must describe either an HTTP endpoint or a Lambda endpoint")
	}
}

// RegisteredDeployment is what registration reports: the new identifier and the services discovery
// found.
type RegisteredDeployment struct {
	ID       string
	Services []Service
	// SDKVersion, MinProtocolVersion and MaxProtocolVersion come from discovery and may be absent.
	SDKVersion         string
	MinProtocolVersion int32
	MaxProtocolVersion int32
	Info               []Notice
}

// Create registers a deployment, which performs service discovery against the endpoint.
//
// Discovery calls the user's own service, so this can be slow and can fail for reasons that have
// nothing to do with Restate. Callers give it a generous timeout through the context.
//
// Retry safety is RetryThrottleOnly. Registration is not idempotent: a retry after an ambiguous 5xx
// could register a second deployment, or trip the `force` behaviour against the deployment the
// first attempt created.
//
// dry_run is deliberately never sent. It is an action -- "tell me what would happen" -- and cannot
// produce a deployment for Terraform to own.
func (d *Deployments) Create(ctx context.Context, input RegisterInput) (RegisteredDeployment, error) {
	kind, err := input.Kind()
	if err != nil {
		return RegisteredDeployment{}, err
	}

	var body generated.CreateDeploymentJSONRequestBody
	switch kind {
	case EndpointHTTP:
		if input.HTTP.URI == "" {
			return RegisteredDeployment{}, errors.New("an HTTP deployment requires a URI")
		}
		variant := generated.RegisterDeploymentRequest0{
			Uri:               input.HTTP.URI,
			UseHttp11:         truePointer(input.HTTP.UseHTTP11),
			Force:             truePointer(input.HTTP.Force),
			Breaking:          truePointer(input.HTTP.Breaking),
			AdditionalHeaders: headerMapPointer(input.HTTP.AdditionalHeaders),
			Metadata:          mapPointer(input.HTTP.Metadata),
		}
		if variant.Auth, err = authRequest(input.HTTP.Auth); err != nil {
			return RegisteredDeployment{}, err
		}
		if err := body.FromRegisterDeploymentRequest0(variant); err != nil {
			return RegisteredDeployment{}, fmt.Errorf("encode HTTP deployment registration: %w", err)
		}
	case EndpointLambda:
		if input.Lambda.ARN == "" {
			return RegisteredDeployment{}, errors.New("a Lambda deployment requires an ARN")
		}
		variant := generated.RegisterDeploymentRequest1{
			Arn:               input.Lambda.ARN,
			AssumeRoleArn:     stringPointer(input.Lambda.AssumeRoleARN),
			Force:             truePointer(input.Lambda.Force),
			Breaking:          truePointer(input.Lambda.Breaking),
			AdditionalHeaders: headerMapPointer(input.Lambda.AdditionalHeaders),
			Metadata:          mapPointer(input.Lambda.Metadata),
		}
		if err := body.FromRegisterDeploymentRequest1(variant); err != nil {
			return RegisteredDeployment{}, fmt.Errorf("encode Lambda deployment registration: %w", err)
		}
	}

	response, err := api.Execute[generated.RegisterDeploymentResponse](ctx, d.client.executor, "CreateDeployment", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return d.client.generated.CreateDeployment(ctx, body)
		})
	if err != nil {
		return RegisteredDeployment{}, err
	}
	if response.Id == "" {
		return RegisteredDeployment{}, &api.Error{
			Service:   api.ServiceAdmin,
			Operation: "CreateDeployment",
			Message:   "the API reported success but returned no deployment ID",
		}
	}

	registered := RegisteredDeployment{
		ID:         response.Id,
		SDKVersion: derefString(response.SdkVersion),
		Info:       noticesFrom(response.Info),
	}
	if response.MinProtocolVersion != nil {
		registered.MinProtocolVersion = *response.MinProtocolVersion
	}
	if response.MaxProtocolVersion != nil {
		registered.MaxProtocolVersion = *response.MaxProtocolVersion
	}
	for _, service := range response.Services {
		registered.Services = append(registered.Services, serviceFrom(service))
	}
	return registered, nil
}

// Get reads one deployment. The response is an untagged union of an HTTP and a Lambda variant, which
// this decodes by looking for the field that only one member has.
func (d *Deployments) Get(ctx context.Context, deploymentID string) (Deployment, error) {
	if deploymentID == "" {
		return Deployment{}, errors.New("a deployment ID is required")
	}

	body, err := api.ExecuteRaw(ctx, d.client.executor, "GetDeployment", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return d.client.generated.GetDeployment(ctx, deploymentID)
		})
	if err != nil {
		return Deployment{}, err
	}
	return decodeDeployment(body, "GetDeployment")
}

// List enumerates every registered deployment.
func (d *Deployments) List(ctx context.Context) ([]Deployment, error) {
	body, err := api.ExecuteRaw(ctx, d.client.executor, "ListDeployments", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return d.client.generated.ListDeployments(ctx)
		})
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Deployments []json.RawMessage `json:"deployments"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, decodeFailure("ListDeployments", err)
	}

	deployments := make([]Deployment, 0, len(envelope.Deployments))
	for _, raw := range envelope.Deployments {
		deployment, err := decodeDeployment(raw, "ListDeployments")
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	return deployments, nil
}

// Update changes a deployment in place.
//
// Without Overwrite the update does not rediscover schemas, so a moved URI keeps the previously
// discovered services. With Overwrite it does rediscover and will accept breaking schema changes.
//
// The contract's update request accepts neither authentication nor metadata. UpdateHTTPDeployment
// and UpdateLambdaDeployment therefore do not carry them, so there is no field here that could be
// silently dropped.
//
// Retry safety is RetryThrottleOnly: an update can trigger rediscovery, which is a side effect
// against the user's own endpoint, and the contract makes no idempotency promise about it.
func (d *Deployments) Update(ctx context.Context, deploymentID string, input UpdateInput) (Deployment, error) {
	if deploymentID == "" {
		return Deployment{}, errors.New("a deployment ID is required")
	}
	kind, err := input.Kind()
	if err != nil {
		return Deployment{}, err
	}

	var body generated.UpdateDeploymentJSONRequestBody
	switch kind {
	case EndpointHTTP:
		variant := generated.UpdateDeploymentRequest0{
			Uri:               stringPointer(input.HTTP.URI),
			UseHttp11:         truePointer(input.HTTP.UseHTTP11),
			Overwrite:         truePointer(input.HTTP.Overwrite),
			AdditionalHeaders: headerMapPointer(input.HTTP.AdditionalHeaders),
		}
		if err := body.FromUpdateDeploymentRequest0(variant); err != nil {
			return Deployment{}, fmt.Errorf("encode HTTP deployment update: %w", err)
		}
	case EndpointLambda:
		variant := generated.UpdateDeploymentRequest1{
			Arn:               stringPointer(input.Lambda.ARN),
			AssumeRoleArn:     stringPointer(input.Lambda.AssumeRoleARN),
			Overwrite:         truePointer(input.Lambda.Overwrite),
			AdditionalHeaders: headerMapPointer(input.Lambda.AdditionalHeaders),
		}
		if err := body.FromUpdateDeploymentRequest1(variant); err != nil {
			return Deployment{}, fmt.Errorf("encode Lambda deployment update: %w", err)
		}
	}

	if err := api.ExecuteNoContent(ctx, d.client.executor, "UpdateDeployment", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return d.client.generated.UpdateDeployment(ctx, deploymentID, body)
		}); err != nil {
		return Deployment{}, err
	}

	// The update response is a detailed deployment, but re-reading is both simpler and more honest:
	// it reports the state a subsequent refresh would see, so an update cannot leave state
	// describing something the read path disagrees with.
	return d.Get(ctx, deploymentID)
}

// Delete removes a deployment.
//
// The pinned contract implements only forced deletion, and forced deletion can put in-flight
// invocations into an unrecoverable error state. Delete therefore refuses unless the caller sets
// Force, rather than quietly doing the destructive thing.
func (d *Deployments) Delete(ctx context.Context, deploymentID string, options DeleteDeploymentOptions) error {
	if deploymentID == "" {
		return errors.New("a deployment ID is required")
	}
	if !options.Force {
		return fmt.Errorf("deleting deployment %s requires forced deletion, which is the only mode the Admin API implements, and which can put in-flight invocations into an unrecoverable state; opt in explicitly to proceed", deploymentID)
	}

	force := true
	err := api.ExecuteNoContent(ctx, d.client.executor, "DeleteDeployment", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return d.client.generated.DeleteDeployment(ctx, deploymentID, &generated.DeleteDeploymentParams{Force: &force})
		})
	if api.IsNotFound(err) {
		return nil
	}
	return err
}

// decodeDeployment resolves the response union.
//
// The two members have no discriminator, so they are told apart by the field only one of them
// carries: `arn` for Lambda and `uri` for HTTP. Both are required in their respective members, so
// exactly one is always present in a well-formed response.
func decodeDeployment(raw []byte, operation string) (Deployment, error) {
	var probe struct {
		ARN *string `json:"arn"`
		URI *string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Deployment{}, decodeFailure(operation, err)
	}

	switch {
	case probe.ARN != nil:
		var response generated.DeploymentResponse1
		if err := json.Unmarshal(raw, &response); err != nil {
			return Deployment{}, decodeFailure(operation, err)
		}
		deployment := Deployment{
			ID:                 response.Id,
			Kind:               EndpointLambda,
			CreatedAt:          response.CreatedAt,
			MinProtocolVersion: response.MinProtocolVersion,
			MaxProtocolVersion: response.MaxProtocolVersion,
			SDKVersion:         derefString(response.SdkVersion),
			Info:               noticesFrom(response.Info),
			Metadata:           derefMap(response.Metadata),
			ARN:                response.Arn,
			AssumeRoleARN:      derefString(response.AssumeRoleArn),
			Services:           serviceRevisionsFrom(response.Services),
		}
		deployment.AdditionalHeaders = headerMapFrom(response.AdditionalHeaders)
		if response.Compression != nil {
			deployment.Compression = LambdaCompression(*response.Compression)
		}
		return deployment, nil

	case probe.URI != nil:
		var response generated.DeploymentResponse0
		if err := json.Unmarshal(raw, &response); err != nil {
			return Deployment{}, decodeFailure(operation, err)
		}
		deployment := Deployment{
			ID:                 response.Id,
			Kind:               EndpointHTTP,
			CreatedAt:          response.CreatedAt,
			MinProtocolVersion: response.MinProtocolVersion,
			MaxProtocolVersion: response.MaxProtocolVersion,
			SDKVersion:         derefString(response.SdkVersion),
			Info:               noticesFrom(response.Info),
			Metadata:           derefMap(response.Metadata),
			URI:                response.Uri,
			HTTPVersion:        response.HttpVersion,
			ProtocolType:       ProtocolType(response.ProtocolType),
			Services:           serviceRevisionsFrom(response.Services),
		}
		deployment.AdditionalHeaders = headerMapFrom(response.AdditionalHeaders)
		auth, err := authFrom(response.Auth)
		if err != nil {
			return Deployment{}, decodeFailure(operation, err)
		}
		deployment.Auth = auth
		return deployment, nil

	default:
		return Deployment{}, &api.Error{
			Service:   api.ServiceAdmin,
			Operation: operation,
			Message:   "the API returned a deployment that is neither an HTTP nor a Lambda endpoint: it has neither a uri nor an arn",
		}
	}
}

func decodeFailure(operation string, err error) error {
	return &api.Error{
		Service:   api.ServiceAdmin,
		Operation: operation,
		Message:   "the API returned a response body that could not be decoded",
		Err:       err,
	}
}

func serviceRevisionsFrom(pairs []generated.ServiceNameRevPair) []ServiceRevision {
	if len(pairs) == 0 {
		return nil
	}
	revisions := make([]ServiceRevision, 0, len(pairs))
	for _, pair := range pairs {
		revisions = append(revisions, ServiceRevision{Name: pair.Name, Revision: pair.Revision})
	}
	return revisions
}

func headerMapPointer(headers map[string]string) *generated.SerdeableHeaderHashMap {
	if headers == nil {
		return nil
	}
	copied := make(generated.SerdeableHeaderHashMap, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	return &copied
}

func headerMapFrom(headers *generated.SerdeableHeaderHashMap) map[string]string {
	if headers == nil {
		return nil
	}
	copied := make(map[string]string, len(*headers))
	for name, value := range *headers {
		copied[name] = value
	}
	return copied
}

// authRequest encodes the deployment authentication union, which is externally tagged:
// `{"GoogleIdToken": {...}}`.
func authRequest(auth *HTTPAuth) (*generated.HttpAuth, error) {
	if auth == nil || auth.GoogleIDToken == nil {
		return nil, nil
	}

	var encoded generated.HttpAuth
	variant := generated.HttpAuth0{
		GoogleIdToken: generated.GoogleIdTokenAuth{
			Audience:                  stringPointer(auth.GoogleIDToken.Audience),
			ImpersonateServiceAccount: stringPointer(auth.GoogleIDToken.ImpersonateServiceAccount),
		},
	}
	if err := encoded.FromHttpAuth0(variant); err != nil {
		return nil, fmt.Errorf("encode deployment authentication: %w", err)
	}
	return &encoded, nil
}

func authFrom(encoded *generated.HttpAuth) (*HTTPAuth, error) {
	if encoded == nil {
		return nil, nil
	}
	// An explicit JSON null decodes into a union whose raw message is empty or "null"; that is
	// "no authentication", not a malformed response.
	raw, err := encoded.MarshalJSON()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	variant, err := encoded.AsHttpAuth0()
	if err != nil {
		return nil, err
	}
	return &HTTPAuth{GoogleIDToken: &GoogleIDTokenAuth{
		Audience:                  derefString(variant.GoogleIdToken.Audience),
		ImpersonateServiceAccount: derefString(variant.GoogleIdToken.ImpersonateServiceAccount),
	}}, nil
}
