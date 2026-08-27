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
		APIServer:    NewAPIServer(t),
		accountID:    accountID,
		environments: map[string]*FakeEnvironment{},
		keys:         map[string]*FakeAPIKey{},
		BillingMode:  "account",
		Regions:      []string{"us-east-1", "eu-central-1"},
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

func (f *CloudFake) register() {
	f.OnFunc("DescribeAccount", func(Request) Response {
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
