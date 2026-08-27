package provider

import (
	"context"
	"fmt"

	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin"
)

// KafkaClusterResource manages a Kafka connection configuration.
type KafkaClusterResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*KafkaClusterResource)(nil)
	_ resource.ResourceWithConfigure   = (*KafkaClusterResource)(nil)
	_ resource.ResourceWithImportState = (*KafkaClusterResource)(nil)
)

// NewKafkaClusterResource returns the resource factory.
func NewKafkaClusterResource() resource.Resource { return &KafkaClusterResource{} }

func (r *KafkaClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kafka_cluster"
}

type kafkaClusterModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Properties types.Map    `tfsdk:"properties"`

	ForceDestroy types.Bool `tfsdk:"force_destroy"`

	CreatedAt types.String `tfsdk:"created_at"`
}

func (r *KafkaClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Kafka cluster connection configuration, which `restate_subscription` references by " +
			"name.\n\n" +
			"`properties` are librdkafka configuration properties and can contain SASL passwords and TLS key " +
			"material, so the whole map is sensitive and is stored in Terraform state. **Use an encrypted, " +
			"access-controlled state backend.**\n\n" +
			"The Admin API redacts sensitive property values when it reads a cluster back, so the provider keeps " +
			"the configured value for any property it cannot verify -- either because the API visibly redacted it, " +
			"or because the property's name marks its value as secret. Non-secret properties are still compared " +
			"against the API, so a genuine out-of-band change is reported as drift.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Same as `name`. The Admin API addresses a cluster by name, so the name is " +
					"the identity; this exists because Terraform tooling expects an `id`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Cluster name, used by subscriptions to reference this cluster. Must be a " +
					"valid hostname.\n\n" +
					"Changing it **replaces** the cluster: the Admin API addresses updates by name, so a rename " +
					"is a different cluster. Any subscription referencing the old name must be updated too.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"properties": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				MarkdownDescription: fmt.Sprintf(
					"librdkafka configuration properties. Must set one of %s to identify the brokers; the "+
						"provider checks this before sending the request.\n\n"+
						"See the [librdkafka configuration reference]"+
						"(https://github.com/confluentinc/librdkafka/blob/master/CONFIGURATION.md) for the full "+
						"list. The whole map is sensitive and is redacted from provider logs and diagnostics.\n\n"+
						"An update replaces the entire map, so a property omitted from a later apply is removed "+
						"from the cluster.",
					quotedList(admin.RequiredBootstrapProperties)),
				Validators: []validator.Map{mapvalidator.SizeAtLeast(1)},
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Delete the cluster even when subscriptions still reference it, **orphaning " +
					"those subscriptions**.\n\n" +
					"By default the provider does not send the API's `force` parameter, leaving the server's own " +
					"dependency check in charge: a delete with attached subscriptions is refused, and the error " +
					"says so. Prefer deleting the subscriptions instead.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the cluster configuration was created.",
			},
		},
	}
}

func (r *KafkaClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *KafkaClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireAdminClient(r.data, "restate_kafka_cluster", &resp.Diagnostics) {
		return
	}

	var plan kafkaClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	properties, diags := stringMap(ctx, plan.Properties)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Validate locally: "properties must set bootstrap.servers" beats whatever the server returns, and
	// costs no round trip.
	if err := admin.ValidateKafkaProperties(properties); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("properties"), "Invalid Kafka cluster properties", err.Error())
		return
	}

	cluster, err := r.data.Admin.KafkaClusters().Create(ctx, admin.KafkaCluster{
		Name:       plan.Name.ValueString(),
		Properties: properties,
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the Kafka cluster", err.Error())
		return
	}
	appendKafkaNotices(&resp.Diagnostics, cluster.Info)

	state := plan
	resp.Diagnostics.Append(r.applyWrite(&state, cluster, properties)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *KafkaClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireAdminClient(r.data, "restate_kafka_cluster", &resp.Diagnostics) {
		return
	}

	var state kafkaClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture the configured properties before the response overwrites anything: they are the only source
	// of truth for a value the API will not disclose.
	configured, diags := stringMap(ctx, state.Properties)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	cluster, err := r.data.Admin.KafkaClusters().Get(ctx, state.Name.ValueString())
	if err != nil {
		if api.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Kafka cluster", err.Error())
		return
	}

	resp.Diagnostics.Append(r.applyRead(ctx, &state, cluster, configured)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *KafkaClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !requireAdminClient(r.data, "restate_kafka_cluster", &resp.Diagnostics) {
		return
	}

	var plan, state kafkaClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	properties, diags := stringMap(ctx, plan.Properties)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := admin.ValidateKafkaProperties(properties); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("properties"), "Invalid Kafka cluster properties", err.Error())
		return
	}

	cluster, err := r.data.Admin.KafkaClusters().Update(ctx, state.Name.ValueString(), properties)
	if err != nil {
		resp.Diagnostics.AddError("Could not update the Kafka cluster", err.Error())
		return
	}
	appendKafkaNotices(&resp.Diagnostics, cluster.Info)

	updated := plan
	resp.Diagnostics.Append(r.applyWrite(&updated, cluster, properties)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *KafkaClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireAdminClient(r.data, "restate_kafka_cluster", &resp.Diagnostics) {
		return
	}

	var state kafkaClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	options := admin.DeleteKafkaClusterOptions{Force: state.ForceDestroy.ValueBool()}
	if err := r.data.Admin.KafkaClusters().Delete(ctx, state.Name.ValueString(), options); err != nil {
		resp.Diagnostics.AddError("Could not delete the Kafka cluster", err.Error())
	}
}

// ImportState takes the cluster name, which is its identity.
func (r *KafkaClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected a Kafka cluster name, for example `orders-cluster`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)

	resp.Diagnostics.AddWarning("An imported Kafka cluster's secret properties are redacted",
		fmt.Sprintf("The Restate Admin API redacts sensitive property values when it reads a cluster back, and an "+
			"import has no configured values to fall back on. State for cluster %s therefore holds the API's "+
			"redacted placeholders -- not real credentials -- for properties such as `sasl.password`.\n\n"+
			"Put the real values in your configuration and apply before relying on this cluster: the next apply "+
			"will send them, replacing the placeholders. Until then `terraform plan` will show a diff on those "+
			"properties, which is correct.", req.ID))
}

// applyRead reconciles state with the API's report, for the refresh path.
//
// The API is not a source of truth for a value it will not disclose, so the merge keeps the configured
// value for anything redacted or sensitively named. That is what makes refresh converge instead of
// either erasing a credential or showing a permanent diff, while still reporting a genuine out-of-band
// change to a non-secret property.
func (r *KafkaClusterResource) applyRead(ctx context.Context, model *kafkaClusterModel, cluster admin.KafkaCluster, configured map[string]string) diag.Diagnostics {
	var diags diag.Diagnostics

	r.applyComputed(model, cluster, applyRead, &diags)
	merged := admin.MergeRedactedProperties(cluster.Properties, configured)
	model.Properties = stringMapValue(ctx, merged, &diags)

	return diags
}

// applyWrite reconciles state after a create or update.
//
// It keeps the planned properties rather than adopting the API's report; see reconcileString. Only
// non-secret properties can be checked, because a redacted value says nothing about what was stored.
func (r *KafkaClusterResource) applyWrite(model *kafkaClusterModel, cluster admin.KafkaCluster, requested map[string]string) diag.Diagnostics {
	var diags diag.Diagnostics

	r.applyComputed(model, cluster, applyWrite, &diags)

	for name, want := range requested {
		if api.IsSensitivePropertyName(name) {
			continue
		}
		got, present := cluster.Properties[name]
		if !present || admin.IsRedactionMarker(got) {
			continue
		}
		if got != want {
			diags.AddAttributeError(path.Root("properties"),
				"The Kafka cluster does not have the requested properties",
				fmt.Sprintf("Cluster %s was asked to set %q to %q, but the API reports %q.",
					cluster.Name, name, want, got))
		}
	}

	return diags
}

func (r *KafkaClusterResource) applyComputed(model *kafkaClusterModel, cluster admin.KafkaCluster, mode applyMode, diags *diag.Diagnostics) {
	model.ID = types.StringValue(cluster.Name)
	model.CreatedAt = types.StringValue(cluster.CreatedAt)
	model.Name = reconcileString(mode, model.Name, cluster.Name,
		path.Root("name"), fmt.Sprintf("Kafka cluster %s", cluster.Name), diags)
}

func appendKafkaNotices(diags *diag.Diagnostics, notices []admin.Notice) {
	for _, notice := range notices {
		detail := notice.Message
		if notice.Code != "" {
			detail = fmt.Sprintf("%s\n\nRestate code: %s", notice.Message, notice.Code)
		}
		diags.AddWarning("Restate reported a notice about the Kafka cluster", detail)
	}
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	return strings.Join(quoted, " or ")
}
