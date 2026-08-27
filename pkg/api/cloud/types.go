package cloud

import (
	"fmt"
	"slices"
	"strings"
)

// The types in this file are the provider's stable view of the Cloud Management API. They exist so
// churn in the generated wire models cannot propagate into Terraform schemas or into any other
// consumer of this package. Adding a field here is a deliberate act; a regenerated client changing
// a field name is not.

// Tier is a Restate Cloud environment tier.
type Tier string

const (
	TierFree     Tier = "FREE"
	TierStarter  Tier = "STARTER"
	TierBusiness Tier = "BUSINESS"
	TierPremium  Tier = "PREMIUM"
	// TierCustom is an operator-configured tier. The provider can read it back on an existing
	// environment, but it cannot be requested: a custom tier is selected through a profile, and the
	// contract does not report an environment's profile, so a custom-tier environment could not be
	// refreshed or imported without hidden drift.
	TierCustom Tier = "CUSTOM"
)

// Tiers lists every tier the pinned contract defines, in ascending order of capability.
func Tiers() []Tier {
	return []Tier{TierFree, TierStarter, TierBusiness, TierPremium, TierCustom}
}

// RequestableTiers lists the tiers a caller may ask for. It excludes CUSTOM; see TierCustom.
func RequestableTiers() []Tier {
	return []Tier{TierFree, TierStarter, TierBusiness, TierPremium}
}

func (t Tier) String() string { return string(t) }

// Valid reports whether t is a tier the pinned contract defines.
func (t Tier) Valid() bool { return slices.Contains(Tiers(), t) }

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

// Terminal reports whether the status will not change without another request. Provisioning polls
// until a status is terminal.
func (s EnvironmentStatus) Terminal() bool {
	return s == StatusActive || s == StatusFailed || s == StatusDeleted
}

// Role is an environment-scoped API key role.
type Role string

const (
	RoleFullAccess              Role = "rst:role::FullAccess"
	RoleIngressAccess           Role = "rst:role::IngressAccess"
	RoleAdminAccess             Role = "rst:role::AdminAccess"
	RoleCompleteAwakeableAccess Role = "rst:role::CompleteAwakeableAccess"
	RoleTunnelAccess            Role = "rst:role::TunnelAccess"

	// RoleManagementAccess is the account-scoped role that authenticates this provider itself. It
	// is deliberately not an environment role: it is bootstrapped outside Terraform, and
	// EnvironmentRoles does not include it so an environment key can never be created with it.
	RoleManagementAccess Role = "rst:role::ManagementApiAccess"
)

// EnvironmentRoles lists the roles an environment-scoped API key may hold.
func EnvironmentRoles() []Role {
	return []Role{RoleFullAccess, RoleIngressAccess, RoleAdminAccess, RoleCompleteAwakeableAccess, RoleTunnelAccess}
}

func (r Role) String() string { return string(r) }

// ValidEnvironmentRole reports whether r may be used for an environment-scoped API key.
func (r Role) ValidEnvironmentRole() bool { return slices.Contains(EnvironmentRoles(), r) }

// ParseEnvironmentRole validates an environment API key role. It rejects the account-scoped
// management role explicitly, because "the management role is not an environment role" is a far
// more useful message than "unknown role".
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

// BillingMode is how an account pays. It decides whether a tier change can complete without a
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

// Environment is the full description of one environment.
//
// Every field other than ID, Name, Region and Tier is server-derived: the contract offers no
// mutation for them, so they are read-only outputs and must never be treated as drift.
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

// EnvironmentSummary is the subset of an environment that list operations return. It has no status
// and no connection metadata, so a caller that needs those must describe the environment.
type EnvironmentSummary struct {
	ID     string
	Name   string
	Region string
	Tier   Tier
}

// CreateEnvironmentInput is a request to provision an environment.
type CreateEnvironmentInput struct {
	// Name is required and must match the contract's pattern: lowercase alphanumerics and hyphens,
	// 2 to 63 characters, not starting or ending with a hyphen.
	Name string
	// Region is optional. When empty the API picks the account's default, which the caller should
	// then persist from the describe response so later plans converge.
	Region string
	// Tier is optional. When empty the API applies its own default.
	Tier Tier
}

// CreatedEnvironment is what the create operation reports. It is deliberately thin: the create
// response is an acknowledgement, not proof that provisioning finished, so a caller must describe
// the environment and poll until its status is terminal.
type CreatedEnvironment struct {
	ID   string
	Name string
}

// Region is a region an account may place environments in.
type Region struct {
	Name        string
	DisplayName string
}

// Account is the non-secret account state the provider can read. The provider does not manage any
// of it; it exists for credential validation and for a read-only data source.
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

	// Secret is the bearer token. The API returns it from create and never again -- not from
	// describe, not from list, not from import. A zero value therefore means "not available from
	// this operation", never "the key has no secret".
	Secret string
}

// CreateAPIKeyInput is a request to mint an environment-scoped API key.
type CreateAPIKeyInput struct {
	EnvironmentID string
	Role          Role
	Description   string
}

// APIKeyRef identifies a key. A key ID is only unique within an environment, so both parts are
// required; a bare key ID cannot address a key.
type APIKeyRef struct {
	EnvironmentID string
	KeyID         string
}

func (r APIKeyRef) String() string {
	return r.EnvironmentID + "/" + r.KeyID
}
