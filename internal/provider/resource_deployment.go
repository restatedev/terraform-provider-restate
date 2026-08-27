package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// Registration and update both perform service discovery against the user's own endpoint, which the
// provider does not control the speed of, so these defaults are generous.
const (
	defaultDeploymentCreateTimeout = 10 * time.Minute
	defaultDeploymentUpdateTimeout = 10 * time.Minute
)

// DeploymentResource manages a registered service deployment.
type DeploymentResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*DeploymentResource)(nil)
	_ resource.ResourceWithConfigure   = (*DeploymentResource)(nil)
	_ resource.ResourceWithImportState = (*DeploymentResource)(nil)
)

// NewDeploymentResource returns the resource factory.
func NewDeploymentResource() resource.Resource { return &DeploymentResource{} }

func (r *DeploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

type deploymentModel struct {
	ID types.String `tfsdk:"id"`

	HTTP   *httpEndpointModel   `tfsdk:"http"`
	Lambda *lambdaEndpointModel `tfsdk:"lambda"`

	Force        types.Bool `tfsdk:"force"`
	Breaking     types.Bool `tfsdk:"breaking"`
	Overwrite    types.Bool `tfsdk:"overwrite"`
	ForceDestroy types.Bool `tfsdk:"force_destroy"`

	EndpointType       types.String `tfsdk:"endpoint_type"`
	CreatedAt          types.String `tfsdk:"created_at"`
	MinProtocolVersion types.Int64  `tfsdk:"min_protocol_version"`
	MaxProtocolVersion types.Int64  `tfsdk:"max_protocol_version"`
	SDKVersion         types.String `tfsdk:"sdk_version"`
	Services           types.List   `tfsdk:"services"`

	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

type httpEndpointModel struct {
	URI               types.String `tfsdk:"uri"`
	UseHTTP11         types.Bool   `tfsdk:"use_http_11"`
	Auth              *authModel   `tfsdk:"auth"`
	AdditionalHeaders types.Map    `tfsdk:"additional_headers"`
	Metadata          types.Map    `tfsdk:"metadata"`

	HTTPVersion  types.String `tfsdk:"http_version"`
	ProtocolType types.String `tfsdk:"protocol_type"`
}

type lambdaEndpointModel struct {
	ARN               types.String `tfsdk:"arn"`
	AssumeRoleARN     types.String `tfsdk:"assume_role_arn"`
	AdditionalHeaders types.Map    `tfsdk:"additional_headers"`
	Metadata          types.Map    `tfsdk:"metadata"`

	Compression types.String `tfsdk:"compression"`
}

type authModel struct {
	GoogleIDToken *googleIDTokenModel `tfsdk:"google_id_token"`
}

type googleIDTokenModel struct {
	Audience                  types.String `tfsdk:"audience"`
	ImpersonateServiceAccount types.String `tfsdk:"impersonate_service_account"`
}

var serviceRevisionAttributes = map[string]attr.Type{
	"name":     types.StringType,
	"revision": types.Int64Type,
}

func (r *DeploymentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A registered Restate service deployment: either an HTTP endpoint or an AWS Lambda " +
			"function.\n\n" +
			"Registering a deployment performs **service discovery**: Restate calls the endpoint to learn which " +
			"services and handlers it exposes. That call goes to your own infrastructure, so it can fail for " +
			"reasons unrelated to Restate and can be slow; set `timeouts` accordingly.\n\n" +
			"Exactly one of `http` and `lambda` must be set. Switching between them replaces the deployment.\n\n" +
			"Deletion is destructive: the Admin API implements only forced deletion, which can put in-flight " +
			"invocations into an unrecoverable state. `force_destroy` must be set before Terraform will destroy " +
			"this resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Deployment ID assigned by Restate, for example `dp_1a2b3c`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"http": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "An HTTP endpoint. Mutually exclusive with `lambda`.",
				Validators: []validator.Object{
					objectvalidator.ExactlyOneOf(path.MatchRoot("http"), path.MatchRoot("lambda")),
				},
				PlanModifiers: []planmodifier.Object{requiresReplaceOnEndpointKindChange()},
				Attributes: map[string]schema.Attribute{
					"uri": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "URI Restate uses to discover and invoke the deployment.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"use_http_11": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						Default:  booldefault.StaticBool(false),
						MarkdownDescription: "Discover using a client that defaults to HTTP/1.1 instead of " +
							"prior-knowledge HTTP/2. A deployment registered this way works only in " +
							"request-response mode.\n\n" +
							"This value is **not read back**: the Admin API reports the negotiated " +
							"`http_version`, and a TLS endpoint that advertises HTTP/2 over ALPN reports HTTP/2 " +
							"even when HTTP/1.1 was requested. The provider therefore keeps the configured " +
							"value and does not treat `http_version` as drift. Read `http_version` to see what " +
							"was actually negotiated.",
					},
					"auth": schema.SingleNestedAttribute{
						Optional: true,
						MarkdownDescription: "Per-deployment authentication.\n\n" +
							"Changing it **replaces** the deployment: the Admin API's update request does not " +
							"accept authentication, so there is no way to change it in place.",
						PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
						Attributes: map[string]schema.Attribute{
							"google_id_token": schema.SingleNestedAttribute{
								Optional: true,
								MarkdownDescription: "Have Restate mint a Google-signed OIDC ID token per request " +
									"and send it as `X-Serverless-Authorization`. This is how a Cloud Run " +
									"deployment authenticates Restate.",
								Attributes: map[string]schema.Attribute{
									"audience": schema.StringAttribute{
										Optional: true,
										MarkdownDescription: "Explicit OIDC `aud` claim. Omit to derive it from " +
											"the deployment URI.",
									},
									"impersonate_service_account": schema.StringAttribute{
										Optional: true,
										MarkdownDescription: "Service account to impersonate when minting the " +
											"token. Omit to use Restate's ambient identity.",
									},
								},
							},
						},
					},
					"additional_headers": schema.MapAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Sensitive:   true,
						MarkdownDescription: "Headers added to every discover and invoke request. Typically API " +
							"keys, so the whole map is treated as sensitive and is redacted from provider logs " +
							"and diagnostics.\n\n" +
							"Values are preserved from configuration on refresh rather than being taken from the " +
							"API, so a header the API declines to echo back cannot silently erase the configured " +
							"credential. Adding, removing, or changing a header is applied in place.",
					},
					"metadata": schema.MapAttribute{
						Optional:    true,
						ElementType: types.StringType,
						MarkdownDescription: "Free-form deployment metadata.\n\n" +
							"Changing it **replaces** the deployment: the Admin API's update request does not " +
							"accept metadata.",
						PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
					},

					"http_version": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "HTTP version Restate negotiated with the endpoint, for example " +
							"`HTTP/2`. This is an outcome, not the read-back of `use_http_11`.",
					},
					"protocol_type": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Service protocol mode: `BidiStream` or `RequestResponse`.",
					},
				},
			},
			"lambda": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "An AWS Lambda endpoint. Mutually exclusive with `http`.",
				PlanModifiers:       []planmodifier.Object{requiresReplaceOnEndpointKindChange()},
				Attributes: map[string]schema.Attribute{
					"arn": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "ARN Restate uses to discover and invoke the Lambda function.",
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"assume_role_arn": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Role Restate assumes when invoking the function, for role chaining. " +
							"Applied in place.",
					},
					"additional_headers": schema.MapAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Sensitive:   true,
						MarkdownDescription: "Headers added to every discover and invoke request. Sensitive, and " +
							"preserved from configuration on refresh; see the `http` variant for details.",
					},
					"metadata": schema.MapAttribute{
						Optional:    true,
						ElementType: types.StringType,
						MarkdownDescription: "Free-form deployment metadata. Changing it **replaces** the " +
							"deployment: the Admin API's update request does not accept metadata.",
						PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
					},

					"compression": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "Response compression Restate negotiated with the function, for " +
							"example `Zstd`. Read-only: the Admin API reports it but accepts it on neither " +
							"registration nor update, so it cannot be configured through this provider.",
					},
				},
			},

			"force": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When registering, override any existing deployment at the same URI or ARN. " +
					"Implies `breaking`, and **can put in-flight invocations into an unrecoverable state**.\n\n" +
					"This is registration behaviour, not deployment state: the Admin API does not report it, so " +
					"it cannot be read back. Changing it does not affect the existing deployment; it applies the " +
					"next time this resource is created. The provider warns rather than silently ignoring the " +
					"change.",
			},
			"breaking": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When registering, allow a service revision whose schema is incompatible with " +
					"the previous one -- including a change of service type.\n\n" +
					"Like `force`, this is registration behaviour that the API does not report and that applies " +
					"only when this resource is created.",
			},
			"overwrite": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When updating, re-run discovery and replace the recorded schema information, " +
					"**allowing breaking changes**.\n\n" +
					"Without it, an update moves the endpoint without rediscovering: the deployment keeps the " +
					"services and handlers it was originally registered with. Set it when the endpoint's schema " +
					"has genuinely changed and you accept the consequences.\n\n" +
					"This is update behaviour, not deployment state, and is not read back.",
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Required before Terraform will destroy this deployment.\n\n" +
					"The Admin API implements only **forced** deletion. Deleting a deployment can leave in-flight " +
					"invocations in an unrecoverable error state, so this has to be set deliberately -- and " +
					"applied -- before a destroy will proceed.",
			},

			"endpoint_type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: fmt.Sprintf("Which endpoint kind this deployment is: `%s` or `%s`. Derived "+
					"from the API response, which carries no discriminator of its own.",
					admin.EndpointHTTP, admin.EndpointLambda),
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the deployment was registered.",
			},
			"min_protocol_version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Lowest service protocol version the endpoint's SDK supports, from discovery.",
			},
			"max_protocol_version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Highest service protocol version the endpoint's SDK supports, from discovery.",
			},
			"sdk_version": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SDK version the endpoint reported during discovery, for example " +
					"`typescript-1.9.0`. Empty when the SDK does not report one.",
			},
			"services": schema.ListNestedAttribute{
				Computed: true,
				MarkdownDescription: "Services this deployment exposes, as discovered. This is discovery output: " +
					"Terraform does not own it, and re-registering the endpoint can change it.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     schema.StringAttribute{Computed: true, MarkdownDescription: "Service name."},
						"revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Service revision this deployment exposes."},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Registration and update perform service discovery against the caller's own endpoint, so
			// those are the operations worth a per-resource timeout. Deletion is a single request, and
			// a read timeout would be advertised and then ignored.
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// requiresReplaceOnEndpointKindChange replaces the deployment when its endpoint kind changes, and only
// then.
//
// The Admin API models HTTP and Lambda deployments as separate union members in both its register and
// its update request, so there is no request that turns one into the other -- but the fields *within*
// a kind are largely updatable, and replacing a deployment because its URI moved would needlessly
// break every in-flight invocation.
//
// So the trigger is the attribute appearing or disappearing, not its contents changing. A per-field
// RequiresReplace handles the fields the update request genuinely cannot carry.
func requiresReplaceOnEndpointKindChange() planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
			// Null-to-set is the initial create, and set-to-null only happens alongside the other
			// endpoint kind becoming set, which its own modifier reports.
			resp.RequiresReplace = req.StateValue.IsNull() != req.PlanValue.IsNull()
		},
		"Changing a deployment's endpoint kind replaces it.",
		"Changing a deployment's endpoint kind replaces it: the Admin API has no request that converts "+
			"an HTTP deployment into a Lambda deployment or the reverse.",
	)
}

func (r *DeploymentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *DeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireAdminClient(r.data, "restate_deployment", &resp.Diagnostics) {
		return
	}

	var plan deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, defaultDeploymentCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	input, diags := r.registerInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	registered, err := r.data.Admin.Deployments().Create(ctx, input)
	if err != nil {
		resp.Diagnostics.AddError("Could not register the Restate deployment",
			fmt.Sprintf("%s\n\n%s", err.Error(), discoveryHint(err, timeout)))
		return
	}
	// Discovery notices explain silently-degraded behaviour, such as a deprecated SDK. They are not
	// errors, but hiding them would leave an operator wondering why something behaves oddly.
	appendNotices(&resp.Diagnostics, "registering", registered.Info)

	// Read back rather than assembling state from the registration response: the read path is what a
	// later refresh sees, so state must describe that.
	deployment, err := r.data.Admin.Deployments().Get(ctx, registered.ID)
	if err != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), registered.ID)...)
		resp.Diagnostics.AddError("Could not read the Restate deployment after registering it",
			fmt.Sprintf("Deployment %s was registered but could not be read back, so its state is incomplete. "+
				"Run `terraform refresh`.\n\n%s", registered.ID, err.Error()))
		return
	}

	state := plan
	resp.Diagnostics.Append(r.applyDeployment(ctx, &state, deployment, applyWrite)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DeploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireAdminClient(r.data, "restate_deployment", &resp.Diagnostics) {
		return
	}

	var state deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deployment, err := r.data.Admin.Deployments().Get(ctx, state.ID.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate deployment", err.Error())
		return
	}

	// An endpoint kind that changed underneath us means this ID now addresses something else
	// entirely. Removing it from state lets the next plan register the deployment that was asked for.
	if r.stateKind(state) != "" && r.stateKind(state) != deployment.Kind {
		resp.Diagnostics.AddWarning("Restate deployment changed endpoint kind",
			fmt.Sprintf("Deployment %s was registered as an %s endpoint but the API now reports it as %s. "+
				"It has been removed from state so the next plan can register the configured deployment.",
				deployment.ID, r.stateKind(state), deployment.Kind))
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(r.applyDeployment(ctx, &state, deployment, applyRead)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DeploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireAdminClient(r.data, "restate_deployment", &resp.Diagnostics) {
		return
	}

	var plan, state deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Update(ctx, defaultDeploymentUpdateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// force and breaking apply only at registration. Saying so beats letting a user believe a
	// flipped flag changed something about the deployment that already exists.
	r.warnAboutRegistrationOnlyChanges(&resp.Diagnostics, plan, state)

	input, diags := r.updateInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	deployment, err := r.data.Admin.Deployments().Update(ctx, state.ID.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError("Could not update the Restate deployment",
			fmt.Sprintf("%s\n\n%s", err.Error(), discoveryHint(err, timeout)))
		return
	}

	if !plan.Overwrite.ValueBool() && r.endpointChanged(plan, state) {
		resp.Diagnostics.AddWarning("The Restate deployment's schema was not rediscovered",
			"The endpoint was updated, but `overwrite` is false, so Restate kept the services and handlers "+
				"discovered when the deployment was first registered. If the endpoint now exposes a different "+
				"schema, set `overwrite = true` -- which permits breaking schema changes -- or register a new "+
				"deployment instead.")
	}

	updated := plan
	resp.Diagnostics.Append(r.applyDeployment(ctx, &updated, deployment, applyWrite)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *DeploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireAdminClient(r.data, "restate_deployment", &resp.Diagnostics) {
		return
	}

	var state deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !state.ForceDestroy.ValueBool() {
		resp.Diagnostics.AddError("Destroying a Restate deployment requires force_destroy",
			fmt.Sprintf("Deployment %s was not deleted because `force_destroy` is false.\n\n"+
				"The Restate Admin API implements only forced deletion, which can leave in-flight invocations in "+
				"an unrecoverable error state. To proceed, set `force_destroy = true`, apply that change, and "+
				"then destroy.", state.ID.ValueString()))
		return
	}

	err := r.data.Admin.Deployments().Delete(ctx, state.ID.ValueString(), admin.DeleteDeploymentOptions{Force: true})
	if err != nil {
		resp.Diagnostics.AddError("Could not delete the Restate deployment", err.Error())
	}
}

// ImportState takes a bare deployment ID: the Admin API is already scoped to one environment by the
// provider's admin_url, so nothing else is needed to address a deployment.
func (r *DeploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected a deployment ID, for example `dp_1a2b3c`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)

	// These four are provider-side behaviour with no server-side representation, so an import cannot
	// discover them. They are set to the schema defaults, which keeps the first plan after import
	// empty for a configuration that does not set them.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force"), false)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("breaking"), false)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("overwrite"), false)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)

	resp.Diagnostics.AddWarning("Some Restate deployment attributes cannot be imported",
		"`force`, `breaking` and `overwrite` describe registration and update behaviour that the Admin API does "+
			"not report, and `use_http_11` cannot be read back because the API reports only the negotiated "+
			"`http_version`. All four have been set to their defaults.\n\n"+
			"`force_destroy` is also false, so `terraform destroy` will refuse until you set it deliberately.\n\n"+
			"Compare `terraform plan` against your configuration before applying.")
}

func (r *DeploymentResource) stateKind(state deploymentModel) admin.EndpointKind {
	switch {
	case state.HTTP != nil:
		return admin.EndpointHTTP
	case state.Lambda != nil:
		return admin.EndpointLambda
	default:
		return ""
	}
}

func (r *DeploymentResource) endpointChanged(plan, state deploymentModel) bool {
	if plan.HTTP != nil && state.HTTP != nil {
		return plan.HTTP.URI.ValueString() != state.HTTP.URI.ValueString() ||
			plan.HTTP.UseHTTP11.ValueBool() != state.HTTP.UseHTTP11.ValueBool()
	}
	if plan.Lambda != nil && state.Lambda != nil {
		return plan.Lambda.ARN.ValueString() != state.Lambda.ARN.ValueString()
	}
	return false
}

func (r *DeploymentResource) warnAboutRegistrationOnlyChanges(diags *diag.Diagnostics, plan, state deploymentModel) {
	for _, candidate := range []struct {
		name         string
		planned, was types.Bool
	}{
		{"force", plan.Force, state.Force},
		{"breaking", plan.Breaking, state.Breaking},
	} {
		if candidate.planned.IsUnknown() || candidate.planned.Equal(candidate.was) {
			continue
		}
		diags.AddAttributeWarning(path.Root(candidate.name),
			fmt.Sprintf("`%s` only applies when a deployment is registered", candidate.name),
			fmt.Sprintf("`%s` changed from %v to %v, but it is registration behaviour rather than deployment "+
				"state: the Restate Admin API does not accept it on an update and does not report it. The new "+
				"value has been recorded and will apply if this resource is created again.",
				candidate.name, candidate.was.ValueBool(), candidate.planned.ValueBool()))
	}
}

// registerInput builds a registration request from the plan.
func (r *DeploymentResource) registerInput(ctx context.Context, plan deploymentModel) (admin.RegisterInput, diag.Diagnostics) {
	var diags diag.Diagnostics

	switch {
	case plan.HTTP != nil:
		headers, headerDiags := stringMap(ctx, plan.HTTP.AdditionalHeaders)
		diags.Append(headerDiags...)
		metadata, metadataDiags := stringMap(ctx, plan.HTTP.Metadata)
		diags.Append(metadataDiags...)

		return admin.RegisterInput{HTTP: &admin.RegisterHTTPDeployment{
			URI:               plan.HTTP.URI.ValueString(),
			UseHTTP11:         plan.HTTP.UseHTTP11.ValueBool(),
			Auth:              authFromModel(plan.HTTP.Auth),
			AdditionalHeaders: headers,
			Metadata:          metadata,
			Force:             plan.Force.ValueBool(),
			Breaking:          plan.Breaking.ValueBool(),
		}}, diags

	case plan.Lambda != nil:
		headers, headerDiags := stringMap(ctx, plan.Lambda.AdditionalHeaders)
		diags.Append(headerDiags...)
		metadata, metadataDiags := stringMap(ctx, plan.Lambda.Metadata)
		diags.Append(metadataDiags...)

		return admin.RegisterInput{Lambda: &admin.RegisterLambdaDeployment{
			ARN:               plan.Lambda.ARN.ValueString(),
			AssumeRoleARN:     plan.Lambda.AssumeRoleARN.ValueString(),
			AdditionalHeaders: headers,
			Metadata:          metadata,
			Force:             plan.Force.ValueBool(),
			Breaking:          plan.Breaking.ValueBool(),
		}}, diags

	default:
		// The ExactlyOneOf validator makes this unreachable through Terraform.
		diags.AddError("No Restate deployment endpoint configured",
			"Exactly one of `http` and `lambda` must be set. This is a bug in the provider; please report it.")
		return admin.RegisterInput{}, diags
	}
}

// updateInput builds an update request. It deliberately carries neither authentication nor metadata:
// the Admin API's update request accepts neither, and changes to them require replacement.
func (r *DeploymentResource) updateInput(ctx context.Context, plan deploymentModel) (admin.UpdateInput, diag.Diagnostics) {
	var diags diag.Diagnostics

	switch {
	case plan.HTTP != nil:
		headers, headerDiags := stringMap(ctx, plan.HTTP.AdditionalHeaders)
		diags.Append(headerDiags...)
		return admin.UpdateInput{HTTP: &admin.UpdateHTTPDeployment{
			URI:               plan.HTTP.URI.ValueString(),
			UseHTTP11:         plan.HTTP.UseHTTP11.ValueBool(),
			AdditionalHeaders: headers,
			Overwrite:         plan.Overwrite.ValueBool(),
		}}, diags

	case plan.Lambda != nil:
		headers, headerDiags := stringMap(ctx, plan.Lambda.AdditionalHeaders)
		diags.Append(headerDiags...)
		return admin.UpdateInput{Lambda: &admin.UpdateLambdaDeployment{
			ARN:               plan.Lambda.ARN.ValueString(),
			AssumeRoleARN:     plan.Lambda.AssumeRoleARN.ValueString(),
			AdditionalHeaders: headers,
			Overwrite:         plan.Overwrite.ValueBool(),
		}}, diags

	default:
		diags.AddError("No Restate deployment endpoint configured",
			"Exactly one of `http` and `lambda` must be set. This is a bug in the provider; please report it.")
		return admin.UpdateInput{}, diags
	}
}

// applyMode says which direction state is being reconciled in.
//
// Terraform requires the value after an apply to equal what was planned, so the write path keeps the
// planned values for configured attributes and verifies the API agrees. Adopting a different value
// there would fail Terraform's consistency check with a "this is a bug in the provider" message that
// says nothing about the real problem, which is a server that did not apply the change.
//
// The read path is the opposite: it adopts the API's values, because that is what makes an
// out-of-band change show up as drift.
type applyMode int

const (
	applyWrite applyMode = iota
	applyRead
)

// applyDeployment copies API state into the model.
func (r *DeploymentResource) applyDeployment(ctx context.Context, model *deploymentModel, deployment admin.Deployment, mode applyMode) diag.Diagnostics {
	var diags diag.Diagnostics

	model.ID = types.StringValue(deployment.ID)
	model.EndpointType = types.StringValue(string(deployment.Kind))
	model.CreatedAt = types.StringValue(deployment.CreatedAt)
	model.MinProtocolVersion = types.Int64Value(int64(deployment.MinProtocolVersion))
	model.MaxProtocolVersion = types.Int64Value(int64(deployment.MaxProtocolVersion))
	model.SDKVersion = types.StringValue(deployment.SDKVersion)

	services := make([]attr.Value, 0, len(deployment.Services))
	for _, service := range deployment.Services {
		object, objectDiags := types.ObjectValue(serviceRevisionAttributes, map[string]attr.Value{
			"name":     types.StringValue(service.Name),
			"revision": types.Int64Value(int64(service.Revision)),
		})
		diags.Append(objectDiags...)
		services = append(services, object)
	}
	serviceList, listDiags := types.ListValue(types.ObjectType{AttrTypes: serviceRevisionAttributes}, services)
	diags.Append(listDiags...)
	model.Services = serviceList

	switch deployment.Kind {
	case admin.EndpointHTTP:
		if model.HTTP == nil {
			model.HTTP = &httpEndpointModel{UseHTTP11: types.BoolValue(false)}
		}
		model.Lambda = nil

		// Always computed: these are outcomes the caller cannot configure.
		model.HTTP.HTTPVersion = types.StringValue(deployment.HTTPVersion)
		model.HTTP.ProtocolType = types.StringValue(string(deployment.ProtocolType))

		// use_http_11 is never touched in either direction: http_version is the negotiated outcome,
		// not the read-back of the request, so there is nothing here to reconcile it against.

		if mode == applyRead {
			model.HTTP.URI = types.StringValue(deployment.URI)
			model.HTTP.AdditionalHeaders = mergeHeaders(ctx, model.HTTP.AdditionalHeaders, deployment.AdditionalHeaders, &diags)
			model.HTTP.Metadata = stringMapValue(ctx, deployment.Metadata, &diags)
		} else if deployment.URI != "" && model.HTTP.URI.ValueString() != deployment.URI {
			diags.AddAttributeError(path.Root("http").AtName("uri"),
				"The Restate deployment does not have the requested URI",
				fmt.Sprintf("Deployment %s was registered with URI %q, but the API reports %q.",
					deployment.ID, model.HTTP.URI.ValueString(), deployment.URI))
		}

		if model.HTTP.Auth == nil {
			// Authentication cannot be updated, so state's value came from configuration at create
			// time. It is reconstructed from the response only when there is nothing to preserve --
			// on an import, say.
			model.HTTP.Auth = authFromDeployment(deployment.Auth)
		}

	case admin.EndpointLambda:
		if model.Lambda == nil {
			model.Lambda = &lambdaEndpointModel{}
		}
		model.HTTP = nil

		model.Lambda.Compression = types.StringValue(string(deployment.Compression))

		if mode == applyRead {
			model.Lambda.ARN = types.StringValue(deployment.ARN)
			model.Lambda.AssumeRoleARN = optionalString(deployment.AssumeRoleARN)
			model.Lambda.AdditionalHeaders = mergeHeaders(ctx, model.Lambda.AdditionalHeaders, deployment.AdditionalHeaders, &diags)
			model.Lambda.Metadata = stringMapValue(ctx, deployment.Metadata, &diags)
		} else if deployment.ARN != "" && model.Lambda.ARN.ValueString() != deployment.ARN {
			diags.AddAttributeError(path.Root("lambda").AtName("arn"),
				"The Restate deployment does not have the requested ARN",
				fmt.Sprintf("Deployment %s was registered with ARN %q, but the API reports %q.",
					deployment.ID, model.Lambda.ARN.ValueString(), deployment.ARN))
		}
	}

	return diags
}

// mergeHeaders reconciles the header map the API reported with what is already in state.
//
// Header values are frequently API keys, and it is not established whether the Admin API echoes them
// back verbatim, redacts them, or omits them. Taking the API's value unconditionally would risk
// overwriting a live credential with a placeholder; ignoring the API entirely would hide a genuine
// out-of-band change. The redaction-aware merge does neither: it keeps the configured value for a
// value that looks redacted or whose header name is sensitive, and takes the API's value otherwise.
func mergeHeaders(ctx context.Context, configured types.Map, remote map[string]string, diags *diag.Diagnostics) types.Map {
	// A configuration with no headers and an API reporting none must stay null, not become an empty
	// map, or the resource would never converge.
	//
	// The null is rebuilt with an explicit element type rather than returning the incoming value:
	// during an import the model field has never been set, so it is a zero types.Map with no element
	// type, which the framework rejects.
	if len(remote) == 0 && (configured.IsNull() || len(configured.Elements()) == 0) {
		if configured.IsNull() || configured.IsUnknown() {
			return types.MapNull(types.StringType)
		}
		return configured
	}

	current, mapDiags := stringMap(ctx, configured)
	diags.Append(mapDiags...)

	merged := admin.MergeRedactedProperties(remote, current)
	return stringMapValue(ctx, merged, diags)
}

func stringMap(ctx context.Context, value types.Map) (map[string]string, diag.Diagnostics) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	result := make(map[string]string, len(value.Elements()))
	diags := value.ElementsAs(ctx, &result, false)
	return result, diags
}

func stringMapValue(ctx context.Context, values map[string]string, diags *diag.Diagnostics) types.Map {
	if values == nil {
		return types.MapNull(types.StringType)
	}
	value, mapDiags := types.MapValueFrom(ctx, types.StringType, values)
	diags.Append(mapDiags...)
	return value
}

func authFromModel(model *authModel) *admin.HTTPAuth {
	if model == nil || model.GoogleIDToken == nil {
		return nil
	}
	return &admin.HTTPAuth{GoogleIDToken: &admin.GoogleIDTokenAuth{
		Audience:                  model.GoogleIDToken.Audience.ValueString(),
		ImpersonateServiceAccount: model.GoogleIDToken.ImpersonateServiceAccount.ValueString(),
	}}
}

func authFromDeployment(auth *admin.HTTPAuth) *authModel {
	if auth == nil || auth.GoogleIDToken == nil {
		return nil
	}
	return &authModel{GoogleIDToken: &googleIDTokenModel{
		Audience:                  optionalString(auth.GoogleIDToken.Audience),
		ImpersonateServiceAccount: optionalString(auth.GoogleIDToken.ImpersonateServiceAccount),
	}}
}

// optionalString keeps an absent value null rather than an empty string, so an unconfigured optional
// attribute does not show a permanent diff against "".
func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// appendNotices surfaces the configuration and deprecation messages the server attaches to an
// object. They are warnings: the operation succeeded, but something about it is worth knowing.
func appendNotices(diags *diag.Diagnostics, operation string, notices []admin.Notice) {
	for _, notice := range notices {
		summary := fmt.Sprintf("Restate reported a notice while %s the deployment", operation)
		detail := notice.Message
		if notice.Code != "" {
			detail = fmt.Sprintf("%s\n\nRestate code: %s", notice.Message, notice.Code)
		}
		diags.AddWarning(summary, detail)
	}
}

// discoveryHint explains a timeout in terms of what actually takes the time, since discovery calls
// the user's own endpoint rather than anything Restate controls.
func discoveryHint(err error, timeout time.Duration) string {
	hint := "Registering or updating a deployment makes Restate call the endpoint to discover its services, so " +
		"this can fail because the endpoint is unreachable from Restate, is slow to respond, or rejected the " +
		"request's authentication."
	if apiErr, ok := api.AsError(err); ok && apiErr.StatusCode == 0 {
		hint += fmt.Sprintf("\n\nThe request did not complete within %s. If discovery is simply slow, raise the "+
			"timeout:\n\n    timeouts {\n      create = \"%s\"\n    }", timeout, 2*timeout)
	}
	return hint
}
