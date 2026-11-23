package cloud

import (
	"fmt"
	"slices"
	"strings"
)

// The types in this file are the provider's stable view of the Cloud Management API, so churn in the
// generated wire models cannot reach a Terraform schema. Adding a field here is a deliberate act; a
// regenerated client renaming one is not.

// Tier is a Restate Cloud environment tier.
type Tier string

const (
	TierFree     Tier = "FREE"
	TierStarter  Tier = "STARTER"
	TierBusiness Tier = "BUSINESS"
	TierPremium  Tier = "PREMIUM"
	// TierCustom is operator-configured. It is readable but not requestable: a custom tier is selected
	// through a profile, which the contract does not report, so requesting one would produce hidden
	// drift on every refresh.
	TierCustom Tier = "CUSTOM"
)

// RequestableTiers lists the tiers a caller may ask for. It excludes CUSTOM; see TierCustom.
func RequestableTiers() []Tier {
	return []Tier{TierFree, TierStarter, TierBusiness, TierPremium}
}

func (t Tier) String() string { return string(t) }

// Requestable reports whether t may be sent in a create or tier-change request.
func (t Tier) Requestable() bool { return slices.Contains(RequestableTiers(), t) }

// EnvironmentStatus is the provisioning state of an environment.
type EnvironmentStatus string

const (
	StatusPending EnvironmentStatus = "PENDING"
	StatusActive  EnvironmentStatus = "ACTIVE"
	StatusFailed  EnvironmentStatus = "FAILED"
	StatusDeleted EnvironmentStatus = "DELETED"
)

// Role is an environment-scoped API key role.
type Role string

const (
	RoleFullAccess              Role = "rst:role::FullAccess"
	RoleIngressAccess           Role = "rst:role::IngressAccess"
	RoleAdminAccess             Role = "rst:role::AdminAccess"
	RoleCompleteAwakeableAccess Role = "rst:role::CompleteAwakeableAccess"
	RoleTunnelAccess            Role = "rst:role::TunnelAccess"

	// RoleManagementAccess is the account-scoped role that authenticates the provider itself. It is
	// deliberately absent from EnvironmentRoles, so an environment key can never be created with it.
	RoleManagementAccess Role = "rst:role::ManagementApiAccess"

	// RoleOrganizationRegionGrantAccess is the organization-scoped role that manages an
	// organization's region grants and builds regions with them in any of its accounts.
	RoleOrganizationRegionGrantAccess Role = "rst:role::OrganizationRegionGrantAccess"
)

// EnvironmentRoles lists the roles an environment-scoped API key may hold.
func EnvironmentRoles() []Role {
	return []Role{RoleFullAccess, RoleIngressAccess, RoleAdminAccess, RoleCompleteAwakeableAccess, RoleTunnelAccess}
}

func (r Role) String() string { return string(r) }

// ValidEnvironmentRole reports whether r may be used for an environment-scoped API key.
func (r Role) ValidEnvironmentRole() bool { return slices.Contains(EnvironmentRoles(), r) }

// ParseEnvironmentRole validates an environment API key role, rejecting the account-scoped management
// role explicitly: that is a far more useful message than "unknown role".
func ParseEnvironmentRole(raw string) (Role, error) {
	role := Role(raw)
	if role.ValidEnvironmentRole() {
		return role, nil
	}
	if role == RoleManagementAccess {
		return "", fmt.Errorf("%s is an account-scoped role and cannot be granted to an environment API key; it is bootstrapped outside Terraform and used as the provider's own credential", RoleManagementAccess)
	}
	names := make([]string, 0, len(EnvironmentRoles()))
	for _, valid := range EnvironmentRoles() {
		names = append(names, valid.String())
	}
	return "", fmt.Errorf("unknown environment API key role %q; expected one of %s", raw, strings.Join(names, ", "))
}

// KeyState is the lifecycle state of an API key.
type KeyState string

const (
	KeyStateActive  KeyState = "ACTIVE"
	KeyStateDeleted KeyState = "DELETED"
)

// BillingMode is how an account pays, which decides whether a tier change can complete without a
// browser: a stripe-billed account may be redirected to interactive checkout.
type BillingMode string

const (
	BillingModeStripe  BillingMode = "stripe"
	BillingModeAccount BillingMode = "account"
)

// SubscriptionStatus is the state of an account's paid subscription.
type SubscriptionStatus string

const (
	SubscriptionAwaitingPayment SubscriptionStatus = "AWAITING_PAYMENT"
	SubscriptionActive          SubscriptionStatus = "ACTIVE"
	SubscriptionInactive        SubscriptionStatus = "INACTIVE"
)

// Environment is the full description of one environment. Every field other than ID, Name, Region and
// Tier is server-derived: the contract offers no mutation for them.
type Environment struct {
	ID     string
	Name   string
	Region string
	// RegionDisplayName is a human-readable region label. The contract marks it optional.
	RegionDisplayName string
	Status            EnvironmentStatus
	Tier              Tier
	SigningPublicKey  string

	IngressBaseURL string
	AdminBaseURL   string
	// InvocationMetricsURL is optional in the contract and is empty when absent.
	InvocationMetricsURL string
	TunnelBaseURL        string
	TunnelSRV            string
	ProxyBaseURL         string

	// APIKeyIDs are the keys the API reports for this environment. They are informational: keys are
	// managed by their own resource, and an environment does not own keys created out of band.
	APIKeyIDs []string
}

// EnvironmentSummary is what list operations return: no status and no connection metadata, so a caller
// needing those must describe the environment.
type EnvironmentSummary struct {
	ID     string
	Name   string
	Region string
	Tier   Tier
}

// CreateEnvironmentInput is a request to provision an environment.
type CreateEnvironmentInput struct {
	// Name is required: lowercase alphanumerics and hyphens, 2 to 63 characters, not starting or
	// ending with one.
	Name string
	// Region is optional. When empty the API picks the account's default, which the caller should then
	// persist so later plans converge.
	Region string
	// Tier is optional. When empty the API applies its own default.
	Tier Tier
}

// CreatedEnvironment is what create reports. It is deliberately thin: the response is an
// acknowledgement, not proof that provisioning finished.
type CreatedEnvironment struct {
	ID   string
	Name string
}

// Region is a region an account may place environments in.
type Region struct {
	Name        string
	DisplayName string
	// Status is set only for the account's own BYOC regions, which are listed while they are built.
	Status string
}

// Account is the non-secret account state the provider can read. Nothing here is managed; it exists
// for credential validation and a read-only data source.
type Account struct {
	ID           string
	Environments []EnvironmentSummary
	// BillingMode is empty when the contract omits it.
	BillingMode BillingMode
	// Subscription is nil for an account with no paid subscription.
	Subscription *Subscription
}

// Subscription is an account's paid subscription state.
type Subscription struct {
	Status SubscriptionStatus
	// CurrentPeriodStart and CurrentPeriodEnd are Unix seconds, as the contract reports them.
	CurrentPeriodStart int64
	CurrentPeriodEnd   int64
}

// Limit is a usage-against-limit pair.
type Limit struct {
	Usage float64
	Limit float64
}

// AccountLimits is an account's quota usage.
type AccountLimits struct {
	FreeEnvironments Limit
	// TierLimits is keyed by tier identifier. It is absent for accounts with no per-tier limits.
	TierLimits map[string]Limit
}

// APIKey is an environment-scoped API key.
type APIKey struct {
	ID            string
	EnvironmentID string
	AccountID     string
	Role          Role
	State         KeyState
	// Description is empty when the key has none.
	Description string

	// Secret is the bearer token. The API returns it from create and never again, so a zero value means
	// "not available from this operation", never "the key has no secret".
	Secret string
}

// CreateAPIKeyInput is a request to mint an environment-scoped API key.
type CreateAPIKeyInput struct {
	EnvironmentID string
	Role          Role
	Description   string
}

// APIKeyRef identifies a key. A key ID is unique only within an environment, so a bare one cannot
// address a key.
type APIKeyRef struct {
	EnvironmentID string
	KeyID         string
}

func (r APIKeyRef) String() string {
	return r.EnvironmentID + "/" + r.KeyID
}

// CloudProvider is the cloud a BYOC region or a region grant lives in.
type CloudProvider string

const (
	CloudAWS   CloudProvider = "aws"
	CloudAzure CloudProvider = "azure"
	CloudGCP   CloudProvider = "gcp"
)

// CloudProviders lists the clouds the API accepts.
func CloudProviders() []CloudProvider { return []CloudProvider{CloudAWS, CloudAzure, CloudGCP} }

// CloudAccount names the one AWS account, GCP project or Azure subscription a region grant or a BYOC
// region lives in. Exactly one field is set, the one for its cloud.
type CloudAccount struct {
	AWSAccountID        string
	GCPProjectID        string
	AzureSubscriptionID string
}

// ID returns whichever identifier is set.
func (a CloudAccount) ID() string {
	switch {
	case a.AWSAccountID != "":
		return a.AWSAccountID
	case a.GCPProjectID != "":
		return a.GCPProjectID
	default:
		return a.AzureSubscriptionID
	}
}

// RegionGrantOwner says whether an account or its organization owns a region grant.
type RegionGrantOwner string

const (
	RegionGrantOwnerAccount      RegionGrantOwner = "ACCOUNT"
	RegionGrantOwnerOrganization RegionGrantOwner = "ORGANIZATION"
)

// RegionGrantStatus is the lifecycle state of a region grant.
type RegionGrantStatus string

const (
	RegionGrantPending RegionGrantStatus = "PENDING"
	RegionGrantReady   RegionGrantStatus = "READY"
	RegionGrantExpired RegionGrantStatus = "EXPIRED"
	RegionGrantDeleted RegionGrantStatus = "DELETED"
)

// Ended reports whether the grant can never become usable again.
func (s RegionGrantStatus) Ended() bool { return s == RegionGrantExpired || s == RegionGrantDeleted }

// RegionGrantInputs are the values the cloud-side grant module is built from. They never change after
// the grant is created.
type RegionGrantInputs struct {
	RestateIssuer  string
	GrantSubject   string
	TokenAudience  string
	ResourcePrefix string
}

// RegionGrant is a grant in one cloud account, owned by an account or an organization, that builds
// any number of BYOC regions there.
type RegionGrant struct {
	ID          string
	Owner       RegionGrantOwner
	OwnerID     string
	DisplayName string
	Cloud       CloudProvider
	Account     CloudAccount
	Status      RegionGrantStatus
	// StatusReason and StatusMessage say why a PENDING grant is not READY yet.
	StatusReason  string
	StatusMessage string
	// Inputs is nil once the grant has ended.
	Inputs    *RegionGrantInputs
	RegionIDs []string
	CreatedAt string
	// ConfirmedAt is empty until the grant is confirmed.
	ConfirmedAt string
	// ExpiresAt is set only while the grant is PENDING.
	ExpiresAt string
}

// CreateRegionGrantInput is a request to create a region grant.
type CreateRegionGrantInput struct {
	// Owner is empty for the API's default, ACCOUNT.
	Owner RegionGrantOwner
	// DisplayName is empty for the API's default.
	DisplayName string
	Cloud       CloudProvider
	Account     CloudAccount
}

// RegionStatus is the lifecycle state of a BYOC region.
type RegionStatus string

const (
	RegionPending  RegionStatus = "PENDING"
	RegionActive   RegionStatus = "ACTIVE"
	RegionFailed   RegionStatus = "FAILED"
	RegionDeleting RegionStatus = "DELETING"
	RegionDeleted  RegionStatus = "DELETED"
)

// ByocRegion is a BYOC region in a customer's cloud account. It is unrelated to Region, which is a
// place an environment may be created in.
type ByocRegion struct {
	ID          string
	Name        string
	DisplayName string
	Cloud       CloudProvider
	CloudRegion string
	Account     CloudAccount
	// MachineType is empty when the API reports none.
	MachineType string
	// GrantID is empty for a region built with a grant of its own.
	GrantID string
	Status  RegionStatus
	// StatusReason is empty when the API reports none. New values may appear, so callers act on Status.
	StatusReason string
	CreatedAt    string
}

// CreateRegionInput is a request to build a BYOC region with a READY region grant.
type CreateRegionInput struct {
	Name string
	// DisplayName is empty for the API's default, the name.
	DisplayName string
	GrantID     string
	Cloud       CloudProvider
	CloudRegion string
	Account     CloudAccount
	// MachineType is empty for the API's default. AWS takes none.
	MachineType string
}

// ByokRegistrationKey is a one-use key that registers one BYOK region, which the customer's own
// Terraform builds. The region exists only once the BYOC chart registers with the key.
type ByokRegistrationKey struct {
	// Key is shown only in the response that mints it. Whoever holds it can register one region for the
	// account until ExpiresAt.
	Key string
	// RegionID is the id the key reserves for the region it registers. It is the BYOC chart's region.id.
	RegionID string
	// ExpiresAt is when the key stops working if no region has used it.
	ExpiresAt string
	// ImagePull is the credential for Restate's registry proxy that the region reserved by this key
	// pulls its images with. It is nil when the control plane serves no images, and for a control
	// plane that predates the field.
	ImagePull *ByokImagePull
}

// ByokImagePull is the registry credential that comes with a BYOK registration key. It is shown only
// in the response that mints the key.
type ByokImagePull struct {
	// Registry is the registry proxy host, such as registry.us.restate.cloud.
	Registry string
	// Username is any value: Docker and crane insist on one.
	Username string
	// Password is a Restate API key for the registry proxy.
	Password string
	// RepositoryPrefix is what a repository name is appended to: the registry host and the stage's prefix.
	RepositoryPrefix string
}

// CreateByokRegistrationKeyInput is a request to mint a BYOK registration key.
type CreateByokRegistrationKeyInput struct {
	// Cloud is the cloud the region's cluster runs on. BYOK supports GCP only.
	Cloud CloudProvider
	// ProviderRegion is the cloud's own name for where the region runs, such as us-central1. The
	// registering region must report the same name.
	ProviderRegion string
	// DisplayName is empty for the API's default.
	DisplayName string
}
