package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// CloudFake is a small stateful model of the Restate Cloud Management API's environment and API key
// lifecycle.
//
// It exists because those lifecycles are asynchronous and self-referential: after a destroy is
// accepted, describing the environment must start reporting it deleted, or the provider's -- correct
// -- decision to poll to a terminal state turns into a test that waits out its timeout. A fixed
// queue of responses cannot express that, and trying to script it produces tests that pass for the
// wrong reason or hang.
//
// The fake deliberately models only what the provider depends on. Anything a test wants to bend --
// a failed provision, an interactive checkout, an out-of-band change -- is done by setting a field
// or reaching into the state directly.
type CloudFake struct {
	*APIServer

	mu sync.Mutex

	accountID string
	// nextID names the next environment or key created, so tests can assert on identifiers.
	environmentCounter int
	keyCounter         int

	environments map[string]*FakeEnvironment
	keys         map[string]*FakeAPIKey

	// BillingMode and Regions are reported by the account and region lookups.
	BillingMode string
	Regions     []string

	// CheckoutURLOnCreate and CheckoutURLOnTierChange make the API demand interactive payment, which
	// Terraform cannot complete.
	CheckoutURLOnCreate     string
	CheckoutURLOnTierChange string

	// FailProvisioning makes a newly created environment settle in FAILED rather than ACTIVE.
	FailProvisioning bool
	// CreatedEnvironmentNameOverride and CreatedAPIKeyRoleOverride simulate successful creates whose
	// read-back disagrees with the request, for partial-state cleanup tests.
	CreatedEnvironmentNameOverride string
	CreatedAPIKeyRoleOverride      string

	// PendingPolls is how many times an environment reports PENDING before becoming ACTIVE, so a
	// test can exercise the create poll loop.
	PendingPolls int

	grantCounter  int
	regionCounter int
	grants        map[string]*FakeRegionGrant
	regions       map[string]*FakeByocRegion

	// OrganizationKey makes the key an organization's region grant key, which cannot describe the
	// account.
	OrganizationKey bool
	// OrganizationID owns the account's ORGANIZATION grants.
	OrganizationID string
	// ConfirmFailure makes the background sign-in end in CONFIRMATION_FAILED with this message.
	ConfirmFailure string
	// RegionPendingPolls is how many describes a new region answers PENDING before it is ACTIVE, and
	// RegionDeletingPolls how many deletes answer 409 while the teardown runs.
	RegionPendingPolls  int
	RegionDeletingPolls int

	byokKeyCounter int
	byokKeys       map[string]*FakeByokKey
	// ByokKeyLimit is how many keys that no region has used the account may hold before the API
	// answers 429. Zero means no limit.
	ByokKeyLimit int
}

// FakeByokKey is one BYOK registration key's state. The region it reserves exists only after
// RegisterByokRegion, as at Restate, where the chart's registration creates it.
type FakeByokKey struct {
	Secret         string
	RegionID       string
	Cloud          string
	ProviderRegion string
	DisplayName    string
	Registered     bool
}

// FakeRegionGrant is one region grant's state.
type FakeRegionGrant struct {
	ID           string
	Owner        string
	OwnerID      string
	DisplayName  string
	Cloud        string
	CloudAccount string
	Status       string
	Output       map[string]string
	// confirming is set while the background sign-in runs, and failure once it has given up.
	confirming bool
	failure    string
}

// FakeByocRegion is one BYOC region's state.
type FakeByocRegion struct {
	ID           string
	Name         string
	DisplayName  string
	GrantID      string
	Cloud        string
	CloudRegion  string
	CloudAccount string
	MachineType  string
	Status       string
	// Environments is how many environments the region holds; a delete is refused while it is not 0.
	Environments int

	pendingPolls  int
	deletingPolls int
}

// FakeEnvironment is one environment's state.
type FakeEnvironment struct {
	ID     string
	Name   string
	Region string
	Tier   string
	Status string

	// pendingPolls counts down the remaining PENDING responses.
	pendingPolls int
}

// FakeAPIKey is one API key's state.
type FakeAPIKey struct {
	ID            string
	EnvironmentID string
	Role          string
	Description   string
	State         string
	Secret        string
}

// NewCloudFake starts a fake Cloud Management API for the given account.
func NewCloudFake(t *testing.T, accountID string) *CloudFake {
	t.Helper()

	fake := &CloudFake{
		APIServer:      NewAPIServer(t),
		accountID:      accountID,
		environments:   map[string]*FakeEnvironment{},
		keys:           map[string]*FakeAPIKey{},
		BillingMode:    "account",
		grants:         map[string]*FakeRegionGrant{},
		regions:        map[string]*FakeByocRegion{},
		byokKeys:       map[string]*FakeByokKey{},
		OrganizationID: "org_1",
		Regions:        []string{"us-east-1", "eu-central-1"},
	}
	fake.register()
	return fake
}

// Environment returns an environment's state so a test can inspect or mutate it directly, which is
// how out-of-band drift is simulated.
func (f *CloudFake) Environment(id string) *FakeEnvironment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.environments[id]
}

// Key returns an API key's state.
func (f *CloudFake) Key(id string) *FakeAPIKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[id]
}

// EnvironmentCount is how many environments currently exist, deleted ones excluded. It is how a test
// asserts that a destroy actually happened.
func (f *CloudFake) EnvironmentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, environment := range f.environments {
		if environment.Status != "DELETED" {
			count++
		}
	}
	return count
}

// KeyCount is how many API keys currently exist, revoked ones excluded.
func (f *CloudFake) KeyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, key := range f.keys {
		if key.State != "DELETED" {
			count++
		}
	}
	return count
}

// Grant returns a region grant's state so a test can inspect or mutate it directly.
func (f *CloudFake) Grant(id string) *FakeRegionGrant {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants[id]
}

// ByocRegion returns a BYOC region's state.
func (f *CloudFake) ByocRegion(id string) *FakeByocRegion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regions[id]
}

func (f *CloudFake) register() {
	f.OnFunc("DescribeAccount", func(Request) Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.OrganizationKey {
			return clientError(http.StatusUnauthorized, "not an account key")
		}

		summaries := make([]map[string]any, 0, len(f.environments))
		for _, environment := range f.environments {
			if environment.Status == "DELETED" {
				continue
			}
			summaries = append(summaries, map[string]any{
				"environmentId": environment.ID,
				"region":        environment.Region,
				"name":          environment.Name,
				"tierId":        environment.Tier,
			})
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"environments": summaries,
			"billingMode":  f.BillingMode,
		})
	})

	f.OnFunc("GetAccountLimits", func(Request) Response {
		return jsonResponse(http.StatusOK, map[string]any{
			"freeEnvironments": map[string]any{"usage": f.EnvironmentCount(), "limit": 3},
		})
	})

	f.OnFunc("ListRegions", func(Request) Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		regions := make([]map[string]any, 0, len(f.Regions))
		for _, region := range f.Regions {
			regions = append(regions, map[string]any{"regionName": region})
		}
		return jsonResponse(http.StatusOK, map[string]any{"regions": regions})
	})

	f.OnFunc("ListEnvironments", func(Request) Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		summaries := make([]map[string]any, 0, len(f.environments))
		for _, environment := range f.environments {
			if environment.Status == "DELETED" {
				continue
			}
			summaries = append(summaries, map[string]any{
				"environmentId": environment.ID,
				"region":        environment.Region,
				"name":          environment.Name,
				"tierId":        environment.Tier,
			})
		}
		return jsonResponse(http.StatusOK, map[string]any{"environments": summaries})
	})

	f.OnFunc("CreateEnvironment", f.createEnvironment)
	f.OnFunc("DescribeEnvironment", f.describeEnvironment)
	f.OnFunc("UpdateEnvironment", f.updateEnvironment)
	f.OnFunc("UpdateEnvironmentTier", f.updateEnvironmentTier)
	f.OnFunc("DestroyEnvironment", f.destroyEnvironment)

	f.OnFunc("CreateRegionGrant", f.createRegionGrant)
	f.OnFunc("ConfirmRegionGrant", f.confirmRegionGrant)
	f.OnFunc("DescribeRegionGrant", f.describeRegionGrant)
	f.OnFunc("ListRegionGrants", f.listRegionGrants)
	f.OnFunc("DeleteRegionGrant", f.deleteRegionGrant)
	f.OnFunc("CreateRegion", f.createRegion)
	f.OnFunc("DescribeRegion", f.describeRegion)
	f.OnFunc("DeleteRegion", f.deleteRegion)

	f.OnFunc("CreateByokRegistrationKey", f.createByokRegistrationKey)
	f.OnFunc("DeleteByokRegion", f.deleteByokRegion)

	f.OnFunc("CreateApiKey", f.createAPIKey)
	f.OnFunc("DescribeApiKey", f.describeAPIKey)
	f.OnFunc("ListApiKeys", f.listAPIKeys)
	f.OnFunc("DeleteApiKey", f.deleteAPIKey)
}

func (f *CloudFake) createEnvironment(request Request) Response {
	var body struct {
		Name   string  `json:"name"`
		Region *string `json:"region"`
		TierID *string `json:"tierId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	if body.Name == "" {
		return clientError(http.StatusBadRequest, "name is required")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.environmentCounter++
	environment := &FakeEnvironment{
		ID:     fmt.Sprintf("env_%d", f.environmentCounter),
		Name:   body.Name,
		Region: "us-east-1",
		Tier:   "FREE",
		Status: "ACTIVE",
	}
	if f.CreatedEnvironmentNameOverride != "" {
		environment.Name = f.CreatedEnvironmentNameOverride
	}
	if body.Region != nil {
		environment.Region = *body.Region
	}
	if body.TierID != nil {
		environment.Tier = *body.TierID
	}
	if f.FailProvisioning {
		environment.Status = "FAILED"
	} else if f.PendingPolls > 0 {
		environment.Status = "PENDING"
		environment.pendingPolls = f.PendingPolls
	}
	f.environments[environment.ID] = environment

	response := map[string]any{"environmentId": environment.ID, "name": environment.Name}
	if f.CheckoutURLOnCreate != "" {
		response["checkoutUrl"] = f.CheckoutURLOnCreate
	}
	return jsonResponse(http.StatusOK, response)
}

func (f *CloudFake) describeEnvironment(request Request) Response {
	id, response := f.environmentIDFrom(request)
	if response != nil {
		return *response
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	environment, ok := f.environments[id]
	if !ok {
		return clientError(http.StatusNotFound, "environment not found")
	}

	// Advance the provisioning state machine one step per describe, which is what makes the
	// provider's create poll loop observable without any real waiting.
	if environment.Status == "PENDING" && environment.pendingPolls > 0 {
		environment.pendingPolls--
		if environment.pendingPolls == 0 {
			environment.Status = "ACTIVE"
		}
	}

	return jsonResponse(http.StatusOK, map[string]any{
		"environmentId":        environment.ID,
		"region":               environment.Region,
		"regionDisplayName":    "US East (N. Virginia)",
		"name":                 environment.Name,
		"status":               environment.Status,
		"signingPublicKey":     "publickeyv1_" + environment.ID,
		"apiKeys":              f.keyRefsFor(environment.ID),
		"ingressBaseUrl":       "https://" + environment.ID + ".env.us.restate.cloud",
		"adminBaseUrl":         "https://" + environment.ID + ".env.us.restate.cloud:9070",
		"invocationMetricsUrl": "https://metrics.us.restate.cloud/" + environment.ID,
		"tunnelBaseUrl":        "https://tunnel.us.restate.cloud",
		"tunnelSrv":            "_tunnel._tcp.us.restate.cloud",
		"proxyBaseUrl":         "https://proxy.us.restate.cloud",
		"tierId":               environment.Tier,
	})
}

func (f *CloudFake) updateEnvironment(request Request) Response {
	var body struct {
		EnvironmentID string `json:"environmentId"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	environment, ok := f.environments[body.EnvironmentID]
	if !ok {
		return clientError(http.StatusNotFound, "environment not found")
	}
	environment.Name = body.Name
	return jsonResponse(http.StatusOK, map[string]any{"environmentId": environment.ID, "name": environment.Name})
}

func (f *CloudFake) updateEnvironmentTier(request Request) Response {
	var body struct {
		EnvironmentID string `json:"environmentId"`
		TierID        string `json:"tierId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	environment, ok := f.environments[body.EnvironmentID]
	if !ok {
		return clientError(http.StatusNotFound, "environment not found")
	}
	if f.CheckoutURLOnTierChange != "" {
		// The tier is deliberately not applied: a checkout URL means the change did not take effect.
		return jsonResponse(http.StatusOK, map[string]any{"checkoutUrl": f.CheckoutURLOnTierChange})
	}
	environment.Tier = body.TierID
	return jsonResponse(http.StatusOK, map[string]any{})
}

func (f *CloudFake) destroyEnvironment(request Request) Response {
	id, response := f.environmentIDFrom(request)
	if response != nil {
		return *response
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	environment, ok := f.environments[id]
	if !ok {
		return clientError(http.StatusNotFound, "environment not found")
	}
	// Teardown is immediate here. The provider still polls, which is what it must do against the real
	// asynchronous API; the fake simply converges on the first poll.
	environment.Status = "DELETED"
	for _, key := range f.keys {
		if key.EnvironmentID == id {
			key.State = "DELETED"
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

func (f *CloudFake) createAPIKey(request Request) Response {
	var body struct {
		EnvironmentID string  `json:"environmentId"`
		RoleID        string  `json:"roleId"`
		Description   *string `json:"description"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.keyCounter++
	key := &FakeAPIKey{
		ID:            fmt.Sprintf("key_%d", f.keyCounter),
		EnvironmentID: body.EnvironmentID,
		Role:          body.RoleID,
		State:         "ACTIVE",
		// A distinct per-key secret, so a test can tell one key's secret from another's after a
		// replacement.
		Secret: fmt.Sprintf("key_secret_%d", f.keyCounter),
	}
	if f.CreatedAPIKeyRoleOverride != "" {
		key.Role = f.CreatedAPIKeyRoleOverride
	}
	if body.Description != nil {
		key.Description = *body.Description
	}
	f.keys[key.ID] = key

	return jsonResponse(http.StatusOK, map[string]any{
		"keyId":         key.ID,
		"roleId":        key.Role,
		"environmentId": key.EnvironmentID,
		"accountId":     f.accountID,
		"apiKey":        key.Secret,
		"state":         key.State,
	})
}

func (f *CloudFake) describeAPIKey(request Request) Response {
	var body struct {
		EnvironmentID string `json:"environmentId"`
		KeyID         string `json:"keyId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key, ok := f.keys[body.KeyID]
	if !ok || key.EnvironmentID != body.EnvironmentID {
		return clientError(http.StatusNotFound, "api key not found")
	}

	// The real API never returns the secret from a describe, which is the whole reason the provider
	// preserves what it already holds. The fake must not return it either, or that behaviour would
	// go untested.
	response := map[string]any{
		"keyId":         key.ID,
		"roleId":        key.Role,
		"environmentId": key.EnvironmentID,
		"accountId":     f.accountID,
		"state":         key.State,
	}
	if key.Description != "" {
		response["description"] = key.Description
	}
	return jsonResponse(http.StatusOK, response)
}

func (f *CloudFake) listAPIKeys(request Request) Response {
	var body struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return jsonResponse(http.StatusOK, map[string]any{"apiKeys": f.keyRefsFor(body.EnvironmentID)})
}

func (f *CloudFake) deleteAPIKey(request Request) Response {
	var body struct {
		EnvironmentID string `json:"environmentId"`
		KeyID         string `json:"keyId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	key, ok := f.keys[body.KeyID]
	if !ok || key.EnvironmentID != body.EnvironmentID {
		return clientError(http.StatusNotFound, "api key not found")
	}
	key.State = "DELETED"
	return jsonResponse(http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// keyRefsFor lists an environment's live keys. The caller must hold the lock.
func (f *CloudFake) keyRefsFor(environmentID string) []map[string]any {
	refs := make([]map[string]any, 0)
	for _, key := range f.keys {
		if key.EnvironmentID == environmentID && key.State != "DELETED" {
			refs = append(refs, map[string]any{"keyId": key.ID, "environmentId": key.EnvironmentID})
		}
	}
	return refs
}

func (f *CloudFake) environmentIDFrom(request Request) (string, *Response) {
	var body struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		response := clientError(http.StatusBadRequest, "malformed request body")
		return "", &response
	}
	if body.EnvironmentID == "" {
		response := clientError(http.StatusBadRequest, "environmentId is required")
		return "", &response
	}
	return body.EnvironmentID, nil
}

func (f *CloudFake) createRegionGrant(request Request) Response {
	var body struct {
		Owner       string            `json:"owner"`
		DisplayName string            `json:"displayName"`
		Cloud       string            `json:"cloud"`
		AWS         map[string]string `json:"aws"`
		GCP         map[string]string `json:"gcp"`
		Azure       map[string]string `json:"azure"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	if body.Owner == "" {
		body.Owner = "ACCOUNT"
	}
	account := body.AWS["accountId"] + body.GCP["projectId"] + body.Azure["subscriptionId"]

	f.mu.Lock()
	defer f.mu.Unlock()
	if (body.Owner == "ACCOUNT") == f.OrganizationKey {
		return clientError(http.StatusForbidden, "the key cannot manage this owner's grants")
	}

	ownerID := f.accountID
	if body.Owner == "ORGANIZATION" {
		ownerID = f.OrganizationID
	}
	for _, grant := range f.grants {
		if grant.OwnerID == ownerID && grant.CloudAccount == account && (grant.Status == "PENDING" || grant.Status == "READY") {
			if body.DisplayName != "" && body.DisplayName != grant.DisplayName {
				return clientError(http.StatusConflict, "the owner has a grant for this cloud account under another display name")
			}
			return jsonResponse(http.StatusOK, f.grantJSON(grant))
		}
	}

	f.grantCounter++
	grant := &FakeRegionGrant{
		ID:           fmt.Sprintf("rgr_%d", f.grantCounter),
		Owner:        body.Owner,
		OwnerID:      ownerID,
		DisplayName:  body.DisplayName,
		Cloud:        body.Cloud,
		CloudAccount: account,
		Status:       "PENDING",
	}
	if grant.DisplayName == "" {
		grant.DisplayName = body.Cloud + " " + account
	}
	f.grants[grant.ID] = grant
	return jsonResponse(http.StatusOK, f.grantJSON(grant))
}

func (f *CloudFake) confirmRegionGrant(request Request) Response {
	var body struct {
		GrantID string            `json:"grantId"`
		Grant   map[string]string `json:"grant"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	grant, ok := f.grants[body.GrantID]
	if !ok {
		return clientError(http.StatusNotFound, "region grant not found")
	}
	switch grant.Status {
	case "EXPIRED", "DELETED":
		return clientError(http.StatusGone, "region grant is "+grant.Status)
	case "READY":
		if fmt.Sprint(grant.Output) != fmt.Sprint(body.Grant) {
			return clientError(http.StatusConflict, "the grant was confirmed with another output")
		}
		return jsonResponse(http.StatusOK, f.grantJSON(grant))
	}
	// Restate signs in through the output in the background: the grant is CONFIRMING now, and the
	// next describe finds it READY or failed.
	grant.Output = body.Grant
	grant.confirming = true
	grant.failure = ""
	return jsonResponse(http.StatusOK, f.grantJSON(grant))
}

func (f *CloudFake) describeRegionGrant(request Request) Response {
	var body struct {
		GrantID string `json:"grantId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	grant, ok := f.grants[body.GrantID]
	if !ok {
		return clientError(http.StatusNotFound, "region grant not found")
	}
	if grant.confirming {
		grant.confirming = false
		if f.ConfirmFailure != "" {
			grant.failure = f.ConfirmFailure
		} else {
			grant.Status = "READY"
		}
	}
	return jsonResponse(http.StatusOK, f.grantJSON(grant))
}

func (f *CloudFake) listRegionGrants(request Request) Response {
	var body struct {
		Owner string `json:"owner"`
	}
	_ = json.Unmarshal([]byte(request.Body), &body)
	if body.Owner == "" {
		body.Owner = "ACCOUNT"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if (body.Owner == "ACCOUNT") == f.OrganizationKey {
		return clientError(http.StatusForbidden, "the key cannot list this owner's grants")
	}
	grants := make([]map[string]any, 0)
	for _, grant := range f.grants {
		if grant.Owner == body.Owner && (grant.Status == "PENDING" || grant.Status == "READY") {
			grants = append(grants, f.grantJSON(grant))
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"grants": grants})
}

func (f *CloudFake) deleteRegionGrant(request Request) Response {
	var body struct {
		GrantID string `json:"grantId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	grant, ok := f.grants[body.GrantID]
	if !ok {
		return clientError(http.StatusNotFound, "region grant not found")
	}
	if len(f.regionIDsOf(grant.ID)) > 0 {
		return clientError(http.StatusConflict, "a region still uses the grant")
	}
	grant.Status = "DELETED"
	return jsonResponse(http.StatusOK, f.grantJSON(grant))
}

// grantJSON renders a grant as the API describes it. The caller must hold the lock.
func (f *CloudFake) grantJSON(grant *FakeRegionGrant) map[string]any {
	described := map[string]any{
		"grantId":     grant.ID,
		"owner":       grant.Owner,
		"ownerId":     grant.OwnerID,
		"displayName": grant.DisplayName,
		"cloud":       grant.Cloud,
		"status":      grant.Status,
		"regionIds":   f.regionIDsOf(grant.ID),
		"createdAt":   "2026-11-12T00:00:00.000Z",
	}
	switch grant.Cloud {
	case "aws":
		described["aws"] = map[string]any{"accountId": grant.CloudAccount}
	case "gcp":
		described["gcp"] = map[string]any{"projectId": grant.CloudAccount}
	case "azure":
		described["azure"] = map[string]any{"subscriptionId": grant.CloudAccount}
	}
	switch grant.Status {
	case "PENDING":
		described["expiresAt"] = "2026-11-19T00:00:00.000Z"
		switch {
		case grant.failure != "":
			described["statusReason"] = "CONFIRMATION_FAILED"
			described["statusMessage"] = grant.failure
		case grant.confirming:
			described["statusReason"] = "CONFIRMING"
		default:
			described["statusReason"] = "AWAITING_CONFIRMATION"
		}
	case "READY":
		described["confirmedAt"] = "2026-11-12T00:01:00.000Z"
	}
	if grant.Status == "PENDING" || grant.Status == "READY" {
		described["grantInputs"] = map[string]any{
			"restateIssuer":  "https://issuer.restate.cloud",
			"grantSubject":   "grant:" + grant.ID,
			"tokenAudience":  "sts.amazonaws.com",
			"resourcePrefix": "rgr-" + grant.ID[len("rgr_"):] + "-",
		}
	}
	return described
}

// regionIDsOf lists the regions built with a grant that are not deleted yet. The caller must hold the
// lock.
func (f *CloudFake) regionIDsOf(grantID string) []string {
	ids := make([]string, 0)
	for _, region := range f.regions {
		if region.GrantID == grantID && region.Status != "DELETED" {
			ids = append(ids, region.ID)
		}
	}
	return ids
}

func (f *CloudFake) createRegion(request Request) Response {
	var body struct {
		Name           string            `json:"name"`
		DisplayName    string            `json:"displayName"`
		Setup          string            `json:"setup"`
		Cloud          string            `json:"cloud"`
		CloudRegion    string            `json:"cloudRegion"`
		AWS            map[string]string `json:"aws"`
		GCP            map[string]string `json:"gcp"`
		Azure          map[string]string `json:"azure"`
		GrantID        string            `json:"grantId"`
		Infrastructure map[string]string `json:"infrastructure"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	account := body.AWS["accountId"] + body.GCP["projectId"] + body.Azure["subscriptionId"]

	f.mu.Lock()
	defer f.mu.Unlock()
	grant, ok := f.grants[body.GrantID]
	if !ok {
		return clientError(http.StatusNotFound, "region grant not found")
	}
	if grant.Status != "READY" {
		return clientError(http.StatusConflict, "region grant is "+grant.Status+", not READY")
	}
	if grant.Cloud != body.Cloud || grant.CloudAccount != account {
		return clientError(http.StatusBadRequest, "region grant builds regions in another cloud account")
	}
	for _, region := range f.regions {
		if region.Name == body.Name && region.Status != "DELETED" {
			return jsonResponse(http.StatusOK, regionJSON(region))
		}
	}

	f.regionCounter++
	region := &FakeByocRegion{
		ID:           fmt.Sprintf("rgn_%d", f.regionCounter),
		Name:         body.Name,
		DisplayName:  body.DisplayName,
		GrantID:      body.GrantID,
		Cloud:        body.Cloud,
		CloudRegion:  body.CloudRegion,
		CloudAccount: account,
		MachineType:  body.Infrastructure["machineType"],
		Status:       "PENDING",
		pendingPolls: f.RegionPendingPolls,
	}
	if region.DisplayName == "" {
		region.DisplayName = region.Name
	}
	if region.MachineType == "" && region.Cloud != "aws" {
		region.MachineType = "e2-standard-4"
	}
	if region.pendingPolls == 0 {
		region.Status = "ACTIVE"
	}
	f.regions[region.ID] = region
	return jsonResponse(http.StatusOK, regionJSON(region))
}

func (f *CloudFake) describeRegion(request Request) Response {
	var body struct {
		RegionID string `json:"regionId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	region, ok := f.regions[body.RegionID]
	if !ok {
		return clientError(http.StatusNotFound, "region not found")
	}
	if region.Status == "PENDING" && region.pendingPolls > 0 {
		region.pendingPolls--
		if region.pendingPolls == 0 {
			region.Status = "ACTIVE"
		}
	}
	return jsonResponse(http.StatusOK, regionJSON(region))
}

func (f *CloudFake) deleteRegion(request Request) Response {
	var body struct {
		RegionID string `json:"regionId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	region, ok := f.regions[body.RegionID]
	if !ok {
		return clientError(http.StatusNotFound, "region not found")
	}
	switch {
	case region.Status == "DELETED":
		return jsonResponse(http.StatusOK, regionJSON(region))
	case region.Environments > 0:
		return clientError(http.StatusConflict, "the region still has environments")
	case region.Status != "DELETING":
		region.Status = "DELETING"
		region.deletingPolls = f.RegionDeletingPolls
	}
	if region.deletingPolls > 0 {
		region.deletingPolls--
		return clientError(http.StatusConflict, region.ID+" is being deleted")
	}
	region.Status = "DELETED"
	return jsonResponse(http.StatusOK, regionJSON(region))
}

// ByokKey returns the registration key that reserved a region id.
func (f *CloudFake) ByokKey(regionID string) *FakeByokKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byokKeys[regionID]
}

// RegisterByokRegion simulates the BYOC chart registering with the key that reserved regionID, which
// is what creates the region at Restate.
func (f *CloudFake) RegisterByokRegion(regionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.byokKeys[regionID]
	key.Registered = true
	f.regions[regionID] = &FakeByocRegion{
		ID:           regionID,
		Name:         regionID,
		DisplayName:  key.DisplayName,
		Cloud:        key.Cloud,
		CloudRegion:  key.ProviderRegion,
		CloudAccount: "customer-project",
		Status:       "ACTIVE",
	}
}

func (f *CloudFake) createByokRegistrationKey(request Request) Response {
	var body struct {
		Cloud             string `json:"cloud"`
		ProviderRegion    string `json:"providerRegion"`
		RegionDisplayName string `json:"regionDisplayName"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	if body.Cloud != "gcp" || body.ProviderRegion == "" {
		return clientError(http.StatusBadRequest, "BYOK supports gcp, and the provider region is required")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	unused := 0
	for _, key := range f.byokKeys {
		if !key.Registered {
			unused++
		}
	}
	if f.ByokKeyLimit > 0 && unused >= f.ByokKeyLimit {
		return clientError(http.StatusTooManyRequests, "the account already holds the most registration keys that no region has used")
	}

	f.byokKeyCounter++
	key := &FakeByokKey{
		Secret:         fmt.Sprintf("rrk_%d.fakesecret%d", f.byokKeyCounter, f.byokKeyCounter),
		RegionID:       fmt.Sprintf("rgn_byok%d", f.byokKeyCounter),
		Cloud:          body.Cloud,
		ProviderRegion: body.ProviderRegion,
		DisplayName:    body.RegionDisplayName,
	}
	f.byokKeys[key.RegionID] = key
	return jsonResponse(http.StatusOK, map[string]any{
		"registrationKey": key.Secret,
		"regionId":        key.RegionID,
		"expiresAt":       "2026-11-18T16:00:00.000Z",
	})
}

// deleteByokRegion answers as the API describes: 404 for a region that does not exist, which includes
// one whose key no chart has used, and 409 while the region hosts environments.
func (f *CloudFake) deleteByokRegion(request Request) Response {
	var body struct {
		RegionID string `json:"regionId"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return clientError(http.StatusBadRequest, "malformed request body")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	region, ok := f.regions[body.RegionID]
	if !ok || f.byokKeys[body.RegionID] == nil {
		return clientError(http.StatusNotFound, "region not found")
	}
	if region.Status == "DELETED" {
		return jsonResponse(http.StatusOK, map[string]any{"regionId": region.ID, "status": "DELETED"})
	}
	if region.Environments > 0 {
		return clientError(http.StatusConflict, "the account still has environments in the region")
	}
	region.Status = "DELETED"
	return jsonResponse(http.StatusOK, map[string]any{"regionId": region.ID, "status": "DELETED"})
}

func regionJSON(region *FakeByocRegion) map[string]any {
	described := map[string]any{
		"regionId":       region.ID,
		"name":           region.Name,
		"displayName":    region.DisplayName,
		"setup":          "GRANT",
		"cloud":          region.Cloud,
		"cloudRegion":    region.CloudRegion,
		"infrastructure": map[string]any{},
		"grantId":        region.GrantID,
		"status":         region.Status,
		"createdAt":      "2026-11-12T00:02:00.000Z",
	}
	if region.MachineType != "" {
		described["infrastructure"] = map[string]any{"machineType": region.MachineType}
	}
	switch region.Cloud {
	case "aws":
		described["aws"] = map[string]any{"accountId": region.CloudAccount}
	case "gcp":
		described["gcp"] = map[string]any{"projectId": region.CloudAccount}
	case "azure":
		described["azure"] = map[string]any{"subscriptionId": region.CloudAccount}
	}
	if region.Status == "PENDING" {
		described["statusReason"] = "CREATING_INFRASTRUCTURE"
	}
	return described
}

func jsonResponse(status int, body map[string]any) Response {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return Response{Status: status, Body: string(encoded)}
}

func clientError(status int, message string) Response {
	return jsonResponse(status, map[string]any{"code": status, "message": message})
}
