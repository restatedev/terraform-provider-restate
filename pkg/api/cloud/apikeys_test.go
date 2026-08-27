package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

func TestParseEnvironmentRoleAcceptsEveryDocumentedRole(t *testing.T) {
	for _, role := range cloud.EnvironmentRoles() {
		got, err := cloud.ParseEnvironmentRole(role.String())
		if err != nil {
			t.Errorf("ParseEnvironmentRole(%q): %v", role, err)
		}
		if got != role {
			t.Errorf("ParseEnvironmentRole(%q) = %q", role, got)
		}
	}
}

// The account-scoped management role must be rejected with an explanation, not with a generic
// "unknown role" that leaves the user guessing why a real role name was refused.
func TestParseEnvironmentRoleRejectsTheManagementRole(t *testing.T) {
	_, err := cloud.ParseEnvironmentRole(cloud.RoleManagementAccess.String())
	if err == nil {
		t.Fatal("the account-scoped management role was accepted as an environment role")
	}
	if !strings.Contains(err.Error(), "account-scoped") {
		t.Errorf("error does not explain why it was refused: %v", err)
	}
}

func TestParseEnvironmentRoleRejectsUnknownRoles(t *testing.T) {
	for _, raw := range []string{"", "FullAccess", "rst:role::Admin", "rst:role::fullaccess", "admin"} {
		if _, err := cloud.ParseEnvironmentRole(raw); err == nil {
			t.Errorf("ParseEnvironmentRole(%q) was accepted", raw)
		}
	}
}

func TestCreateAPIKeyReturnsTheSecretExactlyOnce(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateApiKey", testutil.JSON(`{
	  "keyId":"key_1a2b","roleId":"rst:role::AdminAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","apiKey":"key_the_actual_secret","state":"ACTIVE"
	}`))

	got, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1",
		Role:          cloud.RoleAdminAccess,
		Description:   "managed by terraform",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.ID != "key_1a2b" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Secret != "key_the_actual_secret" {
		t.Errorf("Secret = %q, want the create response's secret", got.Secret)
	}
	if got.Role != cloud.RoleAdminAccess {
		t.Errorf("Role = %q", got.Role)
	}
	if got.State != cloud.KeyStateActive {
		t.Errorf("State = %q", got.State)
	}
	// The contract's create response omits the description, so the facade carries it over from the
	// request rather than reporting it as empty and inventing drift.
	if got.Description != "managed by terraform" {
		t.Errorf("Description = %q, want the configured value carried over", got.Description)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["roleId"] != "rst:role::AdminAccess" {
		t.Errorf("roleId = %v", body["roleId"])
	}
	if body["description"] != "managed by terraform" {
		t.Errorf("description = %v", body["description"])
	}
}

func TestCreateAPIKeyOmitsAnAbsentDescription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateApiKey", testutil.JSON(`{
	  "keyId":"key_1","roleId":"rst:role::IngressAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","apiKey":"key_secret","state":"ACTIVE"
	}`))

	if _, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1",
		Role:          cloud.RoleIngressAccess,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if _, present := body["description"]; present {
		t.Errorf("description was sent despite not being configured: %v", body["description"])
	}
}

// This is the boundary that must make minting an account-scoped management key through the
// environment key path impossible, regardless of what a caller passes in.
func TestCreateAPIKeyRefusesTheManagementRole(t *testing.T) {
	server := testutil.NewAPIServer(t)
	_, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1",
		Role:          cloud.RoleManagementAccess,
	})
	if err == nil {
		t.Fatal("the management role was accepted")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent for the management role")
	}
}

func TestCreateAPIKeyValidatesItsArguments(t *testing.T) {
	server := testutil.NewAPIServer(t)
	keys := newClient(t, server).APIKeys()

	if _, err := keys.Create(context.Background(), cloud.CreateAPIKeyInput{Role: cloud.RoleFullAccess}); err == nil {
		t.Error("expected an error for a missing environment ID")
	}
	if _, err := keys.Create(context.Background(), cloud.CreateAPIKeyInput{EnvironmentID: "env_1"}); err == nil {
		t.Error("expected an error for a missing role")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

// A key with no recoverable secret is useless, so a create response missing it is a failure even
// though the key now exists. The identifier must survive so it can be cleaned up.
func TestCreateAPIKeyRejectsAResponseWithNoSecret(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateApiKey", testutil.JSON(`{
	  "keyId":"key_orphan","roleId":"rst:role::FullAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","apiKey":"","state":"ACTIVE"
	}`))

	got, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1", Role: cloud.RoleFullAccess,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got.ID != "key_orphan" {
		t.Errorf("ID = %q, want the identifier preserved for cleanup", got.ID)
	}
	if !strings.Contains(err.Error(), "key_orphan") {
		t.Errorf("error does not name the key: %v", err)
	}
}

func TestCreateAPIKeyRejectsAResponseWithNoIdentifier(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateApiKey", testutil.JSON(`{
	  "keyId":"","roleId":"rst:role::FullAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","apiKey":"key_secret","state":"ACTIVE"
	}`))

	if _, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1", Role: cloud.RoleFullAccess,
	}); err == nil {
		t.Fatal("expected an error")
	}
}

// Describe never returns the secret. The facade must report an empty Secret so a caller can tell
// "not available from this operation" from a real value, and keep whatever it already has.
func TestGetAPIKeyReportsNoSecret(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeApiKey", testutil.JSON(`{
	  "keyId":"key_1","roleId":"rst:role::AdminAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","state":"ACTIVE","description":"ci"
	}`))

	got, err := newClient(t, server).APIKeys().Get(context.Background(), cloud.APIKeyRef{
		EnvironmentID: "env_1", KeyID: "key_1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Secret != "" {
		t.Errorf("Secret = %q, want empty: describe cannot return the bearer token", got.Secret)
	}
	if got.Description != "ci" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.Role != cloud.RoleAdminAccess {
		t.Errorf("Role = %q", got.Role)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["environmentId"] != "env_1" || body["keyId"] != "key_1" {
		t.Errorf("request body = %v: a key is addressed by environment and key ID together", body)
	}
}

func TestGetAPIKeyToleratesAnAbsentDescription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeApiKey", testutil.JSON(`{
	  "keyId":"key_1","roleId":"rst:role::FullAccess","environmentId":"env_1",
	  "accountId":"acc_1a2b3c","state":"ACTIVE"
	}`))

	got, err := newClient(t, server).APIKeys().Get(context.Background(), cloud.APIKeyRef{
		EnvironmentID: "env_1", KeyID: "key_1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty", got.Description)
	}
}

func TestGetAPIKeyRequiresBothIdentifiers(t *testing.T) {
	server := testutil.NewAPIServer(t)
	keys := newClient(t, server).APIKeys()

	if _, err := keys.Get(context.Background(), cloud.APIKeyRef{KeyID: "key_1"}); err == nil {
		t.Error("expected an error for a missing environment ID")
	}
	if _, err := keys.Get(context.Background(), cloud.APIKeyRef{EnvironmentID: "env_1"}); err == nil {
		t.Error("expected an error for a missing key ID")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

func TestGetActiveAPIKeyTreatsBothFormsOfAbsenceAsGone(t *testing.T) {
	tests := []struct {
		name     string
		response testutil.Response
	}{
		{"not found", testutil.Error(http.StatusNotFound, "key not found")},
		{"reported as deleted", testutil.JSON(`{
		  "keyId":"key_1","roleId":"rst:role::FullAccess","environmentId":"env_1",
		  "accountId":"acc_1a2b3c","state":"DELETED"
		}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DescribeApiKey", tt.response)

			_, err := newClient(t, server).APIKeys().GetActive(context.Background(), cloud.APIKeyRef{
				EnvironmentID: "env_1", KeyID: "key_1",
			})
			if !errors.Is(err, cloud.ErrAPIKeyGone) {
				t.Fatalf("err = %v, want ErrAPIKeyGone", err)
			}
			if !strings.Contains(err.Error(), "key_1") {
				t.Errorf("error does not name the key: %v", err)
			}
		})
	}
}

func TestListAPIKeysForEnvironment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("ListApiKeys", testutil.JSON(`{"apiKeys":[
	  {"keyId":"key_1","environmentId":"env_1"},
	  {"keyId":"key_2","environmentId":"env_1"}
	]}`))

	got, err := newClient(t, server).APIKeys().ListForEnvironment(context.Background(), "env_1")
	if err != nil {
		t.Fatalf("ListForEnvironment: %v", err)
	}
	want := []cloud.APIKeyRef{
		{EnvironmentID: "env_1", KeyID: "key_1"},
		{EnvironmentID: "env_1", KeyID: "key_2"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListAPIKeysRequiresAnEnvironment(t *testing.T) {
	server := testutil.NewAPIServer(t)
	if _, err := newClient(t, server).APIKeys().ListForEnvironment(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent without an environment ID")
	}
}

func TestDeleteAPIKey(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteApiKey", testutil.JSON(`{"result":{"ok":true}}`))

	ref := cloud.APIKeyRef{EnvironmentID: "env_1", KeyID: "key_1"}
	if err := newClient(t, server).APIKeys().Delete(context.Background(), ref, struct{}{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["environmentId"] != "env_1" || body["keyId"] != "key_1" {
		t.Errorf("request body = %v", body)
	}
}

func TestDeleteAPIKeyTreatsNotFoundAsSuccess(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteApiKey", testutil.Error(http.StatusNotFound, "key not found"))

	ref := cloud.APIKeyRef{EnvironmentID: "env_1", KeyID: "key_1"}
	if err := newClient(t, server).APIKeys().Delete(context.Background(), ref, struct{}{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDeleteAPIKeyRequiresBothIdentifiers(t *testing.T) {
	server := testutil.NewAPIServer(t)
	keys := newClient(t, server).APIKeys()

	if err := keys.Delete(context.Background(), cloud.APIKeyRef{KeyID: "key_1"}, struct{}{}); err == nil {
		t.Error("expected an error for a missing environment ID")
	}
	if err := keys.Delete(context.Background(), cloud.APIKeyRef{EnvironmentID: "env_1"}, struct{}{}); err == nil {
		t.Error("expected an error for a missing key ID")
	}
	if len(server.Requests()) != 0 {
		t.Error("a request was sent despite invalid arguments")
	}
}

// A created key's secret is the most sensitive value this provider handles. No failure path may put
// it into an error string.
func TestAPIKeyErrorsNeverContainTheSecret(t *testing.T) {
	const secret = "key_live_the_actual_bearer_secret"
	server := testutil.NewAPIServer(t)
	server.On("CreateApiKey", testutil.Error(http.StatusBadRequest,
		"role rejected for key with secret Bearer "+secret))

	_, err := newClient(t, server).APIKeys().Create(context.Background(), cloud.CreateAPIKeyInput{
		EnvironmentID: "env_1", Role: cloud.RoleFullAccess,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked the secret: %v", err)
	}
	if !strings.Contains(err.Error(), api.Redacted) {
		t.Errorf("error is missing the redaction placeholder: %v", err)
	}
}
