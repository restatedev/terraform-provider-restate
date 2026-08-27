package admin

import (
	"fmt"
	"slices"
)

// The types in this file are the provider's stable view of the Admin API. The generated client's
// untagged `oneOf` unions, pointer-wrapped optionals, and Rust-derived naming stop here.

// EndpointKind is how a deployment is reached. The contract models HTTP and Lambda deployments as
// an untagged union, which has no discriminator on the wire; this is the provider's explicit tag.
type EndpointKind string

const (
	EndpointHTTP   EndpointKind = "http"
	EndpointLambda EndpointKind = "lambda"
)

// ServiceType is the kind of a discovered service.
type ServiceType string

const (
	ServiceTypeService       ServiceType = "Service"
	ServiceTypeVirtualObject ServiceType = "VirtualObject"
	ServiceTypeWorkflow      ServiceType = "Workflow"
)

// SupportsWorkflowCompletionRetention reports whether workflow_completion_retention is meaningful
// for this service type. Setting it on a non-workflow service is a configuration error, and the
// service type is how that is diagnosed.
func (t ServiceType) SupportsWorkflowCompletionRetention() bool { return t == ServiceTypeWorkflow }

// HandlerType is the concurrency model of a handler.
type HandlerType string

const (
	HandlerExclusive HandlerType = "Exclusive"
	HandlerShared    HandlerType = "Shared"
	HandlerWorkflow  HandlerType = "Workflow"
)

// ProtocolType is how Restate talks to a deployment.
type ProtocolType string

const (
	ProtocolBidiStream      ProtocolType = "BidiStream"
	ProtocolRequestResponse ProtocolType = "RequestResponse"
)

// LambdaCompression is a supported Lambda response compression. The pinned contract defines only
// Zstd, and only on the read path; see Deployment.Compression.
type LambdaCompression string

const LambdaCompressionZstd LambdaCompression = "Zstd"

// GoogleIDTokenAuth configures Restate to mint a Google-signed OIDC token per request, which is how
// a Cloud Run deployment authenticates callers.
type GoogleIDTokenAuth struct {
	// Audience is the explicit OIDC `aud` claim. Empty means derive it from the deployment URL.
	Audience string
	// ImpersonateServiceAccount is the service account to impersonate. Empty means use the ambient
	// identity.
	ImpersonateServiceAccount string
}

// HTTPAuth is a deployment's optional authentication configuration. It is a union in the contract
// with one member today; the pointer field keeps room for another without changing this type's
// shape.
type HTTPAuth struct {
	GoogleIDToken *GoogleIDTokenAuth
}

// ServiceRevision names a service and the revision a deployment exposes.
type ServiceRevision struct {
	Name     string
	Revision int32
}

// Deployment is a registered service deployment.
//
// Kind decides which of the two field groups is meaningful. Everything except URI, UseHTTP11, Auth,
// AdditionalHeaders, Metadata, ARN and AssumeRoleARN is server-derived and must be treated as a
// read-only output: the contract offers no mutation for it, and discovery can change it whenever a
// deployment is re-registered.
type Deployment struct {
	ID   string
	Kind EndpointKind

	CreatedAt          string
	MinProtocolVersion int32
	MaxProtocolVersion int32
	// SDKVersion is reported by discovery and is empty when the SDK does not send one.
	SDKVersion string
	// Services are the services this deployment exposes, from discovery.
	Services []ServiceRevision
	// Info carries configuration and deprecation notices the server attached to this deployment.
	Info []Notice

	// AdditionalHeaders are sent on every discover and invoke request. Values can be API keys, so
	// they are sensitive at every level.
	AdditionalHeaders map[string]string
	// Metadata is free-form deployment metadata.
	Metadata map[string]string

	// URI is set for EndpointHTTP.
	URI string
	// HTTPVersion is the negotiated HTTP version, for example "HTTP/1.1" or "HTTP/2".
	//
	// It is not the same thing as the `use_http_11` request field. A TLS deployment that advertises
	// HTTP/2 via ALPN reports HTTP/2 even when HTTP/1.1 was requested, so this cannot be used to
	// read back what was configured; it is an output only.
	HTTPVersion string
	// ProtocolType is set for EndpointHTTP.
	ProtocolType ProtocolType
	// Auth is set for EndpointHTTP when the deployment has authentication configured.
	Auth *HTTPAuth

	// ARN is set for EndpointLambda.
	ARN string
	// AssumeRoleARN is set for EndpointLambda when role chaining is configured.
	AssumeRoleARN string
	// Compression is set for EndpointLambda when the deployment negotiated compression.
	//
	// The pinned contract reports it on read but accepts it on neither register nor update, so it is
	// an output only. It cannot be configured through this provider.
	Compression LambdaCompression
}

// Notice is a configuration or deprecation message the server attached to an object.
type Notice struct {
	Code    string
	Message string
}

// RegisterHTTPDeployment registers an HTTP deployment.
type RegisterHTTPDeployment struct {
	// URI is required.
	URI string
	// UseHTTP11 requests discovery with an HTTP/1.1 client. A deployment registered this way works
	// only in request-response mode.
	UseHTTP11 bool
	Auth      *HTTPAuth
	// AdditionalHeaders and Metadata are optional. A nil map is omitted from the request; an empty
	// map is sent as an empty object, which the API treats as "no headers".
	AdditionalHeaders map[string]string
	Metadata          map[string]string

	// Force overrides an existing deployment at the same URI. It implies Breaking, and it can put
	// in-flight invocations into an unrecoverable state.
	Force bool
	// Breaking permits registering a revision whose schema is incompatible with the previous one.
	Breaking bool
}

// RegisterLambdaDeployment registers an AWS Lambda deployment.
type RegisterLambdaDeployment struct {
	// ARN is required.
	ARN string
	// AssumeRoleARN optionally chains into another role when invoking the Lambda.
	AssumeRoleARN     string
	AdditionalHeaders map[string]string
	Metadata          map[string]string

	Force    bool
	Breaking bool
}

// UpdateHTTPDeployment changes an existing HTTP deployment in place.
//
// The pinned contract's update request exposes neither authentication nor metadata, so a change to
// either cannot be applied by an update and must replace the deployment. This type therefore
// deliberately omits both rather than accepting values it would silently drop.
type UpdateHTTPDeployment struct {
	// URI, when non-empty, moves the deployment.
	URI string
	// UseHTTP11 requests rediscovery with an HTTP/1.1 client.
	UseHTTP11 bool
	// AdditionalHeaders replaces the header map. Nil leaves it unchanged.
	AdditionalHeaders map[string]string
	// Overwrite permits the update to replace discovered schema information, including breaking
	// changes. Without it, an update does not rediscover schemas.
	Overwrite bool
}

// UpdateLambdaDeployment changes an existing Lambda deployment in place. As with the HTTP variant,
// the contract's update request cannot change metadata.
type UpdateLambdaDeployment struct {
	// ARN, when non-empty, repoints the deployment.
	ARN string
	// AssumeRoleARN, when non-empty, changes role chaining.
	AssumeRoleARN string
	// AdditionalHeaders replaces the header map. Nil leaves it unchanged.
	AdditionalHeaders map[string]string
	Overwrite         bool
}

// DeleteDeploymentOptions gates deployment deletion.
type DeleteDeploymentOptions struct {
	// Force must be true for the delete to be attempted. The pinned contract implements only forced
	// deletion, and a forced delete can put in-flight invocations into an unrecoverable state, so
	// the caller has to say so explicitly rather than discovering it afterwards.
	Force bool
}

// KafkaCluster is a Kafka connection configuration.
type KafkaCluster struct {
	Name string
	// Properties are librdkafka configuration properties. They can contain SASL passwords and TLS
	// key material, so the whole map is sensitive.
	//
	// The API redacts sensitive values on read, so a value read back is not necessarily the value
	// that was configured. See MergeRedactedProperties.
	Properties map[string]string
	CreatedAt  string
	Info       []Notice
	// Subscriptions is populated only when the read asked for it.
	Subscriptions []Subscription
}

// RequiredBootstrapProperties are the librdkafka properties that can identify the brokers. The API
// requires at least one of them, and rejecting the request locally produces a better message than
// a server-side validation error.
var RequiredBootstrapProperties = []string{"bootstrap.servers", "metadata.broker.list"}

// ValidateKafkaProperties checks the constraint the contract documents: a cluster must name its
// brokers.
func ValidateKafkaProperties(properties map[string]string) error {
	for _, name := range RequiredBootstrapProperties {
		if value, ok := properties[name]; ok && value != "" {
			return nil
		}
	}
	return fmt.Errorf("Kafka cluster properties must set one of %v to identify the brokers", RequiredBootstrapProperties)
}

// DeleteKafkaClusterOptions gates Kafka cluster deletion.
type DeleteKafkaClusterOptions struct {
	// Force deletes the cluster even though subscriptions still reference it, which orphans them.
	// It defaults to false so the API's own dependency check is the default behaviour.
	Force bool
}

// Subscription connects a Kafka topic to a handler.
type Subscription struct {
	ID     string
	Source string
	Sink   string
	// Options are subscription options, passed through as strings.
	Options map[string]string
}

// Service is a discovered service and its effective configuration.
//
// A service is created and destroyed by deployment discovery, never by this provider. The retention
// and timeout fields are durations in either the friendly or ISO-8601 form the server accepts.
type Service struct {
	Name         string
	Type         ServiceType
	Revision     int32
	DeploymentID string
	// Documentation comes from the SDK.
	Documentation string
	Metadata      map[string]string
	Info          []Notice
	Handlers      []Handler

	// Public and the duration fields below are the subset of service configuration the contract's
	// modify request accepts. A nil pointer means the server reported nothing for the field, which
	// is distinct from a zero value.
	Public                      *bool
	IdempotencyRetention        string
	JournalRetention            string
	WorkflowCompletionRetention string
	InactivityTimeout           string
	AbortTimeout                string

	// EnableLazyState is reported by the API but is not accepted by its modify request, so it is an
	// output only.
	EnableLazyState *bool
	// RetryPolicy is reported but not modifiable through the contract's modify request.
	RetryPolicy *RetryPolicy
}

// Handler is one handler of a service. Every field is discovery output; the contract has no handler
// modification endpoint.
type Handler struct {
	Name              string
	Type              HandlerType
	Documentation     string
	InputDescription  string
	OutputDescription string
	Metadata          map[string]string
	Info              []Notice

	Public               *bool
	IdempotencyRetention string
	JournalRetention     string
	InactivityTimeout    string
	AbortTimeout         string
	EnableLazyState      *bool
	RetryPolicy          *RetryPolicy
}

// RetryPolicy is a service or handler retry policy, as reported by the API.
type RetryPolicy struct {
	InitialInterval      string
	MaxInterval          string
	MaxAttempts          *int
	ExponentiationFactor *float64
	OnMaxAttempts        string
}

// ModifyService is the subset of service configuration the contract's modify request accepts.
//
// Every field is a pointer so "leave this alone" is distinguishable from "set this to the zero
// value". The contract does not document what a JSON null means for these fields, so this facade
// never sends one: an unset field is omitted.
type ModifyService struct {
	Public                      *bool
	IdempotencyRetention        *string
	JournalRetention            *string
	WorkflowCompletionRetention *string
	InactivityTimeout           *string
	AbortTimeout                *string
}

// IsEmpty reports whether the request would change nothing, so a caller can skip a pointless call.
func (m ModifyService) IsEmpty() bool {
	return m.Public == nil && m.IdempotencyRetention == nil && m.JournalRetention == nil &&
		m.WorkflowCompletionRetention == nil && m.InactivityTimeout == nil && m.AbortTimeout == nil
}

// ServerInfo is what GET /version reports.
type ServerInfo struct {
	// Version is the Restate server version, for example "1.7.6".
	Version string
	// MinAdminAPIVersion and MaxAdminAPIVersion bound the Admin API versions the server accepts.
	MinAdminAPIVersion int32
	MaxAdminAPIVersion int32
	// IngressEndpoint is the ingress address the server advertises. It is empty when the server
	// reports none.
	IngressEndpoint string
	// Features lists the server's experimental features and whether each is enabled.
	Features map[string]bool
}

// KafkaSourceScheme and ServiceSinkScheme are the URI schemes the documentation gives for
// subscription endpoints. They are used for advisory validation, not as a closed enum: a newer
// server may advertise a scheme this pinned contract has never heard of, and rejecting it outright
// would make the provider the reason a supported configuration cannot be expressed.
const (
	KafkaSourceScheme = "kafka"
	ServiceSinkScheme = "service"
)

// KnownSourceSchemes and KnownSinkSchemes list the schemes this contract snapshot documents.
func KnownSourceSchemes() []string { return []string{KafkaSourceScheme} }
func KnownSinkSchemes() []string   { return []string{ServiceSinkScheme} }

// IsKnownSourceScheme and IsKnownSinkScheme report whether a scheme is one this snapshot documents.
// A false result is a reason to warn, not to reject.
func IsKnownSourceScheme(scheme string) bool { return slices.Contains(KnownSourceSchemes(), scheme) }
func IsKnownSinkScheme(scheme string) bool   { return slices.Contains(KnownSinkSchemes(), scheme) }
