package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

// CloudAPIKeyResource manages an environment-scoped Restate Cloud API key.
type CloudAPIKeyResource struct {
	data *providerData
}

var (
	_ resource.Resource                = (*CloudAPIKeyResource)(nil)
	_ resource.ResourceWithConfigure   = (*CloudAPIKeyResource)(nil)
	_ resource.ResourceWithImportState = (*CloudAPIKeyResource)(nil)
)

// NewCloudAPIKeyResource returns the resource factory.
func NewCloudAPIKeyResource() resource.Resource { return &CloudAPIKeyResource{} }

func (r *CloudAPIKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloud_api_key"
}

type cloudAPIKeyModel struct {
	ID            types.String `tfsdk:"id"`
	AccountID     types.String `tfsdk:"account_id"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	RoleID        types.String `tfsdk:"role_id"`
	Description   types.String `tfsdk:"description"`

	State  types.String `tfsdk:"state"`
	APIKey types.String `tfsdk:"api_key"`
}

func (r *CloudAPIKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An environment-scoped Restate Cloud API key.\n\n" +
			"The bearer secret is returned by the create call and never again: it cannot be read back, and it " +
			"cannot be recovered by an import. It is stored in Terraform state, marked sensitive. **Terraform " +
			"state therefore contains a live credential** -- use an encrypted, access-controlled state backend.\n\n" +
			"The Cloud Management API has no update operation for keys, so changing any attribute replaces the " +
			"key. Replacement mints a new secret and revokes the old one, which breaks anything still " +
			"authenticating with it. Use `create_before_destroy` if a consumer needs an overlap.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Key ID assigned by Restate Cloud. A key ID is unique within an environment, not globally.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account the key belongs to. It is the first part of the import ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"environment_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Environment the key grants access to. Changing it replaces the key: a key is " +
					"minted for one environment and cannot be moved.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"role_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: fmt.Sprintf(
					"Role the key grants: one of %s.\n\n"+
						"`%s` is an account-scoped role and is rejected here: it is the provider's own credential, "+
						"is bootstrapped outside Terraform, and granting it to an environment key would be a "+
						"privilege escalation.\n\n"+
						"Changing the role replaces the key, because the API has no update operation.",
					quotedEnvironmentRoles(), cloud.RoleManagementAccess),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.OneOf(environmentRoleNames()...)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Free-text description, useful for identifying a key in the Restate Cloud " +
					"console. Changing it replaces the key: the API has no update operation, so there is no way to " +
					"edit a description in place. Omit it instead of setting an empty string.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},

			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Key lifecycle state: `ACTIVE` or `DELETED`. A key reported as `DELETED` is removed from state.",
			},
			"api_key": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				MarkdownDescription: "The bearer secret. Available only from the create response.\n\n" +
					"It is null for an imported key, because the API cannot return it. A configuration that needs " +
					"the secret must either supply it separately or replace the key with " +
					"`terraform taint`/`-replace`.",
				PlanModifiers: []planmodifier.String{
					// Without this, every plan would show api_key as "(known after apply)" and a
					// refresh could replace the stored secret with an unknown value.
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *CloudAPIKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *CloudAPIKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireCloudClient(r.data, "restate_cloud_api_key", &resp.Diagnostics) {
		return
	}

	var plan cloudAPIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The schema validator already rejects the management role, but the facade validates too, and
	// this converts its error into an attribute-level diagnostic rather than a generic one.
	role, err := cloud.ParseEnvironmentRole(plan.RoleID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("role_id"), "Invalid Restate Cloud API key role", err.Error())
		return
	}

	key, err := r.data.Cloud.APIKeys().Create(ctx, cloud.CreateAPIKeyInput{
		EnvironmentID: plan.EnvironmentID.ValueString(),
		Role:          role,
		Description:   plan.Description.ValueString(),
	})
	if err != nil {
		// A key whose secret was not returned still exists. Record its ID so Terraform can revoke
		// it rather than leaving an untracked credential behind.
		if key.ID != "" {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), key.ID)...)
			environmentID := plan.EnvironmentID.ValueString()
			if key.EnvironmentID != "" {
				environmentID = key.EnvironmentID
			}
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), environmentID)...)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), r.data.Cloud.AccountID())...)
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_id"), plan.RoleID)...)
		}
		resp.Diagnostics.AddError("Could not create the Restate Cloud API key", err.Error())
		return
	}

	state := plan
	r.applyKey(&state, key, applyWrite, &resp.Diagnostics)
	// The secret comes from this response and nowhere else.
	state.APIKey = types.StringValue(key.Secret)
	if resp.Diagnostics.HasError() && key.EnvironmentID != "" {
		// Keys are deleted through their environment scope. If verification found a mismatched scope,
		// retain the server's scope so the newly minted credential can still be revoked.
		state.EnvironmentID = types.StringValue(key.EnvironmentID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CloudAPIKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !requireCloudClient(r.data, "restate_cloud_api_key", &resp.Diagnostics) {
		return
	}

	var state cloudAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ref := cloud.APIKeyRef{
		EnvironmentID: state.EnvironmentID.ValueString(),
		KeyID:         state.ID.ValueString(),
	}
	key, err := r.data.Cloud.APIKeys().GetActive(ctx, ref)
	if err != nil {
		if errors.Is(err, cloud.ErrAPIKeyGone) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read the Restate Cloud API key", err.Error())
		return
	}

	// Describe never returns the secret, so the value already in state is kept. Overwriting it with
	// the response's empty value would destroy the only copy of a live credential.
	secret := state.APIKey
	r.applyKey(&state, key, applyRead, &resp.Diagnostics)
	state.APIKey = secret

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update exists only to satisfy the interface. Every configurable attribute requires replacement,
// because the Cloud Management API has no key update operation, so Terraform never calls this.
func (r *CloudAPIKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Restate Cloud API keys cannot be updated in place",
		"The Cloud Management API has no update operation for API keys, so every change replaces the key. "+
			"Reaching this point means the provider's plan modifiers are wrong; please report it as a bug.")
}

func (r *CloudAPIKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !requireCloudClient(r.data, "restate_cloud_api_key", &resp.Diagnostics) {
		return
	}

	var state cloudAPIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ref := cloud.APIKeyRef{
		EnvironmentID: state.EnvironmentID.ValueString(),
		KeyID:         state.ID.ValueString(),
	}
	if err := r.data.Cloud.APIKeys().Delete(ctx, ref, struct{}{}); err != nil {
		resp.Diagnostics.AddError("Could not delete the Restate Cloud API key", err.Error())
	}
}

// ImportState accepts "<account ID>/<environment ID>/<key ID>".
//
// A key ID is only unique within an environment, so the environment is part of the identity rather
// than something the provider could look up.
func (r *CloudAPIKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	fields, ok := splitImportID(req.ID, 3)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<account ID>/<environment ID>/<key ID>`, for example "+
				"`acc_1a2b3c/env_1a2b3c/key_1a2b3c`, but got %q.", req.ID))
		return
	}
	accountID, environmentID, keyID := fields[0], fields[1], fields[2]

	if !requireCloudClient(r.data, "restate_cloud_api_key", &resp.Diagnostics) {
		return
	}
	if configured := r.data.Cloud.AccountID(); accountID != configured {
		resp.Diagnostics.AddError("Import ID does not match the configured account",
			fmt.Sprintf("The import ID names account %s, but this provider is configured for account %s. "+
				"Use a provider alias configured for the right account, or correct the import ID.", accountID, configured))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), keyID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), environmentID)...)

	// The secret cannot be recovered. Null is the honest value, and the warning tells the user what
	// that means before they discover it downstream.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_key"), types.StringNull())...)
	resp.Diagnostics.AddWarning("The imported API key's secret is not available",
		fmt.Sprintf("Restate Cloud returns an API key's bearer secret only when the key is created, so `api_key` "+
			"is null for imported key %s.\n\n"+
			"Terraform can now observe and delete this key, and the next plan will be empty. Anything that needs "+
			"the secret must be given it another way, or you must replace the key with "+
			"`terraform apply -replace=<address>` to mint a new one -- which revokes this one.", keyID))
}

// applyKey copies API state into the model. See reconcileString for why a write and a refresh treat
// the configured attributes differently.
func (r *CloudAPIKeyResource) applyKey(model *cloudAPIKeyModel, key cloud.APIKey, mode applyMode, diags *diag.Diagnostics) {
	subject := fmt.Sprintf("API key %s", key.ID)

	model.ID = types.StringValue(key.ID)
	model.AccountID = types.StringValue(key.AccountID)
	model.State = types.StringValue(string(key.State))

	model.EnvironmentID = reconcileString(mode, model.EnvironmentID, key.EnvironmentID, path.Root("environment_id"), subject, diags)
	model.RoleID = reconcileString(mode, model.RoleID, key.Role.String(), path.Root("role_id"), subject, diags)

	// An absent description must stay null rather than becoming "", or a resource with no configured
	// description would show a permanent diff. reconcileString is not used here because it cannot
	// express that: null and "" are the same value to the API but different to Terraform.
	if mode == applyRead {
		if key.Description == "" {
			model.Description = types.StringNull()
		} else {
			model.Description = types.StringValue(key.Description)
		}
	}
}

func quotedEnvironmentRoles() string {
	names := make([]string, 0, len(cloud.EnvironmentRoles()))
	for _, role := range cloud.EnvironmentRoles() {
		names = append(names, "`"+role.String()+"`")
	}
	return strings.Join(names, ", ")
}

func environmentRoleNames() []string {
	names := make([]string, 0, len(cloud.EnvironmentRoles()))
	for _, role := range cloud.EnvironmentRoles() {
		names = append(names, role.String())
	}
	return names
}
