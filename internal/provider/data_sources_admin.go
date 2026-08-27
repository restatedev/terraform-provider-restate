package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// The lookups in this file are focused single-object reads rather than list data sources. A list
// data source is only worth adding once there is a Terraform expression that needs one; until then it
// is a larger surface to keep compatible for no demonstrated benefit.

// ServiceDataSource looks up a discovered service.
type ServiceDataSource struct{ data *providerData }

var (
	_ datasource.DataSource              = (*ServiceDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*ServiceDataSource)(nil)
)

// NewServiceDataSource returns the data source factory.
func NewServiceDataSource() datasource.DataSource { return &ServiceDataSource{} }

func (d *ServiceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

type serviceDataSourceModel struct {
	Name         types.String `tfsdk:"name"`
	Type         types.String `tfsdk:"type"`
	Revision     types.Int64  `tfsdk:"revision"`
	DeploymentID types.String `tfsdk:"deployment_id"`

	Documentation types.String `tfsdk:"documentation"`
	Public        types.Bool   `tfsdk:"public"`

	IdempotencyRetention        types.String `tfsdk:"idempotency_retention"`
	JournalRetention            types.String `tfsdk:"journal_retention"`
	WorkflowCompletionRetention types.String `tfsdk:"workflow_completion_retention"`
	InactivityTimeout           types.String `tfsdk:"inactivity_timeout"`
	AbortTimeout                types.String `tfsdk:"abort_timeout"`

	Metadata     types.Map  `tfsdk:"metadata"`
	HandlerNames types.List `tfsdk:"handler_names"`
}

func (d *ServiceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service discovered from a registered deployment, with its effective " +
			"configuration.\n\n" +
			"Services are created and destroyed by deployment discovery, never by Terraform. This data source " +
			"reads one; it does not manage it.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Service name to look up, as reported by discovery.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Service kind: `%s`, `%s`, or `%s`. Only a `%s` supports "+
					"`workflow_completion_retention`.",
					admin.ServiceTypeService, admin.ServiceTypeVirtualObject, admin.ServiceTypeWorkflow, admin.ServiceTypeWorkflow),
			},
			"revision": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Current service revision. Discovery increments it when the schema changes.",
			},
			"deployment_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Deployment currently serving this service.",
			},
			"documentation": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Documentation the SDK reported for this service. Empty when it reported none.",
			},
			"public": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the service is reachable through the environment's ingress. Null when the API reports nothing.",
			},
			"idempotency_retention":         schema.StringAttribute{Computed: true, MarkdownDescription: "How long completed idempotent invocations are retained."},
			"journal_retention":             schema.StringAttribute{Computed: true, MarkdownDescription: "How long invocation journals are retained."},
			"workflow_completion_retention": schema.StringAttribute{Computed: true, MarkdownDescription: "How long completed workflow results are retained. Meaningful only for a workflow service."},
			"inactivity_timeout":            schema.StringAttribute{Computed: true, MarkdownDescription: "How long Restate waits before asking a stalled invocation to suspend."},
			"abort_timeout":                 schema.StringAttribute{Computed: true, MarkdownDescription: "How long Restate waits after the inactivity timeout before aborting an invocation."},
			"metadata": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Service metadata reported by discovery.",
			},
			"handler_names": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Names of the service's handlers. Use `restate_service_handler` for a handler's own configuration.",
			},
		},
	}
}

func (d *ServiceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *ServiceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_service", &resp.Diagnostics) {
		return
	}

	var config serviceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	service, err := d.data.Admin.Services().Get(ctx, config.Name.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.Diagnostics.AddError("Restate service not found",
				fmt.Sprintf("No service named %q has been discovered in this environment.\n\n"+
					"A service exists only once a deployment exposing it has been registered. Check that the "+
					"deployment was registered and that discovery found this service.", config.Name.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate service", err.Error())
		return
	}

	handlerNames := make([]attr.Value, 0, len(service.Handlers))
	for _, handler := range service.Handlers {
		handlerNames = append(handlerNames, types.StringValue(handler.Name))
	}
	handlerList, diags := types.ListValue(types.StringType, handlerNames)
	resp.Diagnostics.Append(diags...)

	state := serviceDataSourceModel{
		Name:                        types.StringValue(service.Name),
		Type:                        types.StringValue(string(service.Type)),
		Revision:                    types.Int64Value(int64(service.Revision)),
		DeploymentID:                types.StringValue(service.DeploymentID),
		Documentation:               types.StringValue(service.Documentation),
		Public:                      optionalBool(service.Public),
		IdempotencyRetention:        types.StringValue(service.IdempotencyRetention),
		JournalRetention:            types.StringValue(service.JournalRetention),
		WorkflowCompletionRetention: types.StringValue(service.WorkflowCompletionRetention),
		InactivityTimeout:           types.StringValue(service.InactivityTimeout),
		AbortTimeout:                types.StringValue(service.AbortTimeout),
		Metadata:                    stringMapValue(ctx, orEmptyStringMap(service.Metadata), &resp.Diagnostics),
		HandlerNames:                handlerList,
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ServiceHandlerDataSource looks up one handler of a service.
type ServiceHandlerDataSource struct{ data *providerData }

var (
	_ datasource.DataSource              = (*ServiceHandlerDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*ServiceHandlerDataSource)(nil)
)

// NewServiceHandlerDataSource returns the data source factory.
func NewServiceHandlerDataSource() datasource.DataSource { return &ServiceHandlerDataSource{} }

func (d *ServiceHandlerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_handler"
}

type serviceHandlerDataSourceModel struct {
	Service types.String `tfsdk:"service"`
	Name    types.String `tfsdk:"name"`

	Type              types.String `tfsdk:"type"`
	Documentation     types.String `tfsdk:"documentation"`
	Public            types.Bool   `tfsdk:"public"`
	InputDescription  types.String `tfsdk:"input_description"`
	OutputDescription types.String `tfsdk:"output_description"`

	IdempotencyRetention types.String `tfsdk:"idempotency_retention"`
	JournalRetention     types.String `tfsdk:"journal_retention"`
	InactivityTimeout    types.String `tfsdk:"inactivity_timeout"`
	AbortTimeout         types.String `tfsdk:"abort_timeout"`

	Metadata types.Map `tfsdk:"metadata"`
}

func (d *ServiceHandlerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One handler of a discovered service.\n\n" +
			"Everything here comes from discovery: the Admin API has no handler modification endpoint, so a " +
			"handler cannot be configured through Terraform. Use it to reference a handler in a " +
			"`restate_subscription` sink and be sure it exists.",
		Attributes: map[string]schema.Attribute{
			"service": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Service the handler belongs to.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Handler name.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Handler concurrency model: `%s`, `%s`, or `%s`. Empty when the "+
					"API reports none.", admin.HandlerExclusive, admin.HandlerShared, admin.HandlerWorkflow),
			},
			"documentation":      schema.StringAttribute{Computed: true, MarkdownDescription: "Documentation the SDK reported for this handler."},
			"public":             schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the handler is reachable through ingress. Null when the API reports nothing."},
			"input_description":  schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable description of the accepted request content types."},
			"output_description": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable description of the response content types."},

			"idempotency_retention": schema.StringAttribute{Computed: true, MarkdownDescription: "Handler-level idempotency retention, when it overrides the service's."},
			"journal_retention":     schema.StringAttribute{Computed: true, MarkdownDescription: "Handler-level journal retention, when it overrides the service's."},
			"inactivity_timeout":    schema.StringAttribute{Computed: true, MarkdownDescription: "Handler-level inactivity timeout, when it overrides the service's."},
			"abort_timeout":         schema.StringAttribute{Computed: true, MarkdownDescription: "Handler-level abort timeout, when it overrides the service's."},

			"metadata": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Handler metadata reported by discovery.",
			},
		},
	}
}

func (d *ServiceHandlerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *ServiceHandlerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_service_handler", &resp.Diagnostics) {
		return
	}

	var config serviceHandlerDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	handler, err := d.data.Admin.Services().GetHandler(ctx, config.Service.ValueString(), config.Name.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.Diagnostics.AddError("Restate service handler not found",
				fmt.Sprintf("Service %q has no handler named %q, or the service itself has not been discovered.",
					config.Service.ValueString(), config.Name.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate service handler", err.Error())
		return
	}

	state := serviceHandlerDataSourceModel{
		Service:              config.Service,
		Name:                 types.StringValue(handler.Name),
		Type:                 types.StringValue(string(handler.Type)),
		Documentation:        types.StringValue(handler.Documentation),
		Public:               optionalBool(handler.Public),
		InputDescription:     types.StringValue(handler.InputDescription),
		OutputDescription:    types.StringValue(handler.OutputDescription),
		IdempotencyRetention: types.StringValue(handler.IdempotencyRetention),
		JournalRetention:     types.StringValue(handler.JournalRetention),
		InactivityTimeout:    types.StringValue(handler.InactivityTimeout),
		AbortTimeout:         types.StringValue(handler.AbortTimeout),
		Metadata:             stringMapValue(ctx, orEmptyStringMap(handler.Metadata), &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// SubscriptionDataSource looks up a subscription by ID.
type SubscriptionDataSource struct{ data *providerData }

var (
	_ datasource.DataSource              = (*SubscriptionDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*SubscriptionDataSource)(nil)
)

// NewSubscriptionDataSource returns the data source factory.
func NewSubscriptionDataSource() datasource.DataSource { return &SubscriptionDataSource{} }

func (d *SubscriptionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription"
}

func (d *SubscriptionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Kafka subscription, looked up by ID. Useful for reading a subscription created " +
			"outside this configuration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Subscription ID to look up, for example `sub_1a2b3c`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"source": schema.StringAttribute{Computed: true, MarkdownDescription: "Kafka topic the subscription consumes."},
			"sink":   schema.StringAttribute{Computed: true, MarkdownDescription: "Handler the subscription invokes."},
			"options": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Consumer options, including any defaults the server applied.",
			},
		},
	}
}

func (d *SubscriptionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *SubscriptionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_subscription", &resp.Diagnostics) {
		return
	}

	var config subscriptionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	subscription, err := d.data.Admin.Subscriptions().Get(ctx, config.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.Diagnostics.AddError("Restate subscription not found",
				fmt.Sprintf("No subscription %s exists in this environment.", config.ID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Could not read the subscription", err.Error())
		return
	}

	state := subscriptionModel{
		ID:      types.StringValue(subscription.ID),
		Source:  types.StringValue(subscription.Source),
		Sink:    types.StringValue(subscription.Sink),
		Options: stringMapValue(ctx, orEmptyStringMap(subscription.Options), &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// KafkaClusterDataSource looks up a Kafka cluster by name.
type KafkaClusterDataSource struct{ data *providerData }

var (
	_ datasource.DataSource              = (*KafkaClusterDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*KafkaClusterDataSource)(nil)
)

// NewKafkaClusterDataSource returns the data source factory.
func NewKafkaClusterDataSource() datasource.DataSource { return &KafkaClusterDataSource{} }

func (d *KafkaClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kafka_cluster"
}

type kafkaClusterDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	Properties types.Map    `tfsdk:"properties"`
	CreatedAt  types.String `tfsdk:"created_at"`

	SubscriptionIDs types.List `tfsdk:"subscription_ids"`
}

func (d *KafkaClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Kafka cluster configuration, looked up by name.\n\n" +
			"**Sensitive property values are redacted by the API**, and this data source has no configured values " +
			"to fall back on, so `properties` reports the API's placeholders for them rather than real " +
			"credentials. Do not feed them to anything expecting a working password.\n\n" +
			"`subscription_ids` is what makes a safe teardown order expressible: a cluster with subscriptions " +
			"attached cannot be deleted without orphaning them.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cluster name to look up.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"properties": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				Sensitive:           true,
				MarkdownDescription: "librdkafka properties, with sensitive values redacted by the API.",
			},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "When the cluster configuration was created."},
			"subscription_ids": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "IDs of the subscriptions that reference this cluster.",
			},
		},
	}
}

func (d *KafkaClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *KafkaClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_kafka_cluster", &resp.Diagnostics) {
		return
	}

	var config kafkaClusterDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, err := d.data.Admin.KafkaClusters().GetWithSubscriptions(ctx, config.Name.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.Diagnostics.AddError("Kafka cluster not found",
				fmt.Sprintf("No Kafka cluster named %q is configured in this environment.", config.Name.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Could not read the Kafka cluster", err.Error())
		return
	}

	subscriptionIDs := make([]attr.Value, 0, len(cluster.Subscriptions))
	for _, subscription := range cluster.Subscriptions {
		subscriptionIDs = append(subscriptionIDs, types.StringValue(subscription.ID))
	}
	subscriptionList, diags := types.ListValue(types.StringType, subscriptionIDs)
	resp.Diagnostics.Append(diags...)

	state := kafkaClusterDataSourceModel{
		Name:            types.StringValue(cluster.Name),
		Properties:      stringMapValue(ctx, orEmptyStringMap(cluster.Properties), &resp.Diagnostics),
		CreatedAt:       types.StringValue(cluster.CreatedAt),
		SubscriptionIDs: subscriptionList,
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// DeploymentDataSource looks up a deployment by ID.
type DeploymentDataSource struct{ data *providerData }

var (
	_ datasource.DataSource              = (*DeploymentDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DeploymentDataSource)(nil)
)

// NewDeploymentDataSource returns the data source factory.
func NewDeploymentDataSource() datasource.DataSource { return &DeploymentDataSource{} }

func (d *DeploymentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

type deploymentDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	EndpointType types.String `tfsdk:"endpoint_type"`

	URI          types.String `tfsdk:"uri"`
	HTTPVersion  types.String `tfsdk:"http_version"`
	ProtocolType types.String `tfsdk:"protocol_type"`

	ARN           types.String `tfsdk:"arn"`
	AssumeRoleARN types.String `tfsdk:"assume_role_arn"`
	Compression   types.String `tfsdk:"compression"`

	CreatedAt          types.String `tfsdk:"created_at"`
	MinProtocolVersion types.Int64  `tfsdk:"min_protocol_version"`
	MaxProtocolVersion types.Int64  `tfsdk:"max_protocol_version"`
	SDKVersion         types.String `tfsdk:"sdk_version"`
	Metadata           types.Map    `tfsdk:"metadata"`
	Services           types.List   `tfsdk:"services"`
}

func (d *DeploymentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A registered deployment, looked up by ID.\n\n" +
			"Unlike the resource, this exposes both endpoint kinds' attributes as a flat set: the ones belonging " +
			"to the other kind are null. Check `endpoint_type` before reading them.\n\n" +
			"`additional_headers` is deliberately **not** exposed. Header values are typically credentials, and " +
			"there is no configured value to reconcile a redacted or omitted one against, so publishing them " +
			"would be misleading at best.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Deployment ID to look up, for example `dp_1a2b3c`.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"endpoint_type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Which endpoint kind this deployment is: `%s` or `%s`.",
					admin.EndpointHTTP, admin.EndpointLambda),
			},

			"uri":           schema.StringAttribute{Computed: true, MarkdownDescription: "HTTP endpoint URI. Null for a Lambda deployment."},
			"http_version":  schema.StringAttribute{Computed: true, MarkdownDescription: "Negotiated HTTP version. Null for a Lambda deployment."},
			"protocol_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Service protocol mode. Null for a Lambda deployment."},

			"arn":             schema.StringAttribute{Computed: true, MarkdownDescription: "Lambda function ARN. Null for an HTTP deployment."},
			"assume_role_arn": schema.StringAttribute{Computed: true, MarkdownDescription: "Role Restate assumes when invoking the function. Null when unset or for an HTTP deployment."},
			"compression":     schema.StringAttribute{Computed: true, MarkdownDescription: "Negotiated Lambda response compression. Null when none or for an HTTP deployment."},

			"created_at":           schema.StringAttribute{Computed: true, MarkdownDescription: "When the deployment was registered."},
			"min_protocol_version": schema.Int64Attribute{Computed: true, MarkdownDescription: "Lowest service protocol version the SDK supports."},
			"max_protocol_version": schema.Int64Attribute{Computed: true, MarkdownDescription: "Highest service protocol version the SDK supports."},
			"sdk_version":          schema.StringAttribute{Computed: true, MarkdownDescription: "SDK version reported during discovery."},
			"metadata": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Deployment metadata.",
			},
			"services": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Services this deployment exposes, as discovered.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     schema.StringAttribute{Computed: true, MarkdownDescription: "Service name."},
						"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Service revision."},
					},
				},
			},
		},
	}
}

func (d *DeploymentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *DeploymentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireAdminClient(d.data, "restate_deployment", &resp.Diagnostics) {
		return
	}

	var config deploymentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deployment, err := d.data.Admin.Deployments().Get(ctx, config.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.Diagnostics.AddError("Restate deployment not found",
				fmt.Sprintf("No deployment %s is registered in this environment.", config.ID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate deployment", err.Error())
		return
	}

	services, diags := serviceRevisionList(deployment)
	resp.Diagnostics.Append(diags...)

	state := deploymentDataSourceModel{
		ID:                 types.StringValue(deployment.ID),
		EndpointType:       types.StringValue(string(deployment.Kind)),
		CreatedAt:          types.StringValue(deployment.CreatedAt),
		MinProtocolVersion: types.Int64Value(int64(deployment.MinProtocolVersion)),
		MaxProtocolVersion: types.Int64Value(int64(deployment.MaxProtocolVersion)),
		SDKVersion:         types.StringValue(deployment.SDKVersion),
		Metadata:           stringMapValue(ctx, orEmptyStringMap(deployment.Metadata), &resp.Diagnostics),
		Services:           services,

		// The attributes belonging to the other endpoint kind stay null, so a configuration reading
		// the wrong one gets nothing rather than a plausible-looking empty string.
		URI:           optionalString(deployment.URI),
		HTTPVersion:   optionalString(deployment.HTTPVersion),
		ProtocolType:  optionalString(string(deployment.ProtocolType)),
		ARN:           optionalString(deployment.ARN),
		AssumeRoleARN: optionalString(deployment.AssumeRoleARN),
		Compression:   optionalString(string(deployment.Compression)),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func serviceRevisionList(deployment admin.Deployment) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	services := make([]attr.Value, 0, len(deployment.Services))
	for _, service := range deployment.Services {
		object, objectDiags := types.ObjectValue(serviceRevisionAttributes, map[string]attr.Value{
			"name":     types.StringValue(service.Name),
			"revision": types.Int64Value(int64(service.Revision)),
		})
		diags.Append(objectDiags...)
		services = append(services, object)
	}
	list, listDiags := types.ListValue(types.ObjectType{AttrTypes: serviceRevisionAttributes}, services)
	diags.Append(listDiags...)
	return list, diags
}

// optionalBool keeps an unreported boolean null rather than false, so "the API said false" stays
// distinguishable from "the API said nothing".
func optionalBool(value *bool) types.Bool {
	if value == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*value)
}

// orEmptyStringMap turns nil into an empty map, so a computed attribute reports "the API answered and
// there was nothing" rather than null.
func orEmptyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
