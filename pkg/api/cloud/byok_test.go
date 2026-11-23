package cloud_test

import (
	"context"
	"strings"
	"testing"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

const byokKeyResponse = `{"registrationKey":"rrk_1a2b3c.4d5e6f","regionId":"rgn_a1b2c3d4e5","expiresAt":"2026-11-18T16:00:00.000Z"}`

func TestCreateByokRegistrationKeySendsTheRegionItIsFor(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.JSON(byokKeyResponse))

	got, err := newClient(t, server).ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud:          cloud.CloudGCP,
		ProviderRegion: "us-central1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Key != "rrk_1a2b3c.4d5e6f" || got.RegionID != "rgn_a1b2c3d4e5" || got.ExpiresAt != "2026-11-18T16:00:00.000Z" {
		t.Errorf("got %+v", got)
	}

	request := server.LastRequest()
	if request.Path != "/"+testAccountID+"/CreateByokRegistrationKey" || request.Authorization != "Bearer "+testKey {
		t.Errorf("request = %s, authorization %q", request.Path, request.Authorization)
	}
	var body map[string]any
	request.DecodeBody(t, &body)
	if body["cloud"] != "gcp" || body["providerRegion"] != "us-central1" {
		t.Errorf("body = %v", body)
	}
	// Restate chooses the name when none is asked for, so an empty one must not be sent: the contract
	// requires at least one character.
	if _, present := body["regionDisplayName"]; present {
		t.Errorf("regionDisplayName was sent despite not being asked for: %v", body)
	}
}

func TestCreateByokRegistrationKeyReturnsTheImagePullCredential(t *testing.T) {
	server := testutil.NewAPIServer(t)
	input := cloud.CreateByokRegistrationKeyInput{Cloud: cloud.CloudGCP, ProviderRegion: "us-central1"}

	server.On("CreateByokRegistrationKey", testutil.JSON(`{"registrationKey":"rrk_1a2b3c.4d5e6f","regionId":"rgn_a1b2c3d4e5","expiresAt":"2026-11-18T16:00:00.000Z",`+
		`"imagePull":{"registry":"registry.us.restate.cloud","username":"restate","password":"key_pull","repositoryPrefix":"registry.us.restate.cloud/us-"}}`))
	got, err := newClient(t, server).ByokRegistrations().Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := cloud.ByokImagePull{Registry: "registry.us.restate.cloud", Username: "restate", Password: "key_pull", RepositoryPrefix: "registry.us.restate.cloud/us-"}
	if got.ImagePull == nil || *got.ImagePull != want {
		t.Errorf("ImagePull = %+v, want %+v", got.ImagePull, want)
	}

	// A control plane that predates the field, or serves no images, answers without it.
	server = testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.JSON(byokKeyResponse))
	got, err = newClient(t, server).ByokRegistrations().Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create without imagePull: %v", err)
	}
	if got.ImagePull != nil || got.Key == "" {
		t.Errorf("a response without imagePull must give a key and no credential, got %+v", got)
	}
}

func TestCreateByokRegistrationKeyRefusesAnIncompleteImagePull(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.JSON(`{"registrationKey":"rrk_1a2b3c.4d5e6f","regionId":"rgn_a1b2c3d4e5","expiresAt":"2026-11-18T16:00:00.000Z",`+
		`"imagePull":{"registry":"registry.us.restate.cloud","username":"restate","password":"","repositoryPrefix":"x"}}`))

	_, err := newClient(t, server).ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud: cloud.CloudGCP, ProviderRegion: "us-central1",
	})
	if err == nil || strings.Contains(err.Error(), "rrk_1a2b3c") {
		t.Errorf("want an error that carries no secret, got %v", err)
	}
}

func TestCreateByokRegistrationKeySendsTheDisplayName(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.JSON(byokKeyResponse))

	_, err := newClient(t, server).ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud:          cloud.CloudGCP,
		ProviderRegion: "us-central1",
		DisplayName:    "customer-a us-central1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["regionDisplayName"] != "customer-a us-central1" {
		t.Errorf("body = %v", body)
	}
}

func TestCreateByokRegistrationKeyRefusesWhatTheAPIWould(t *testing.T) {
	server := testutil.NewAPIServer(t)
	client := newClient(t, server).ByokRegistrations()

	for name, input := range map[string]cloud.CreateByokRegistrationKeyInput{
		"no provider region": {Cloud: cloud.CloudGCP},
		"aws":                {Cloud: cloud.CloudAWS, ProviderRegion: "eu-central-1"},
	} {
		if _, err := client.Create(context.Background(), input); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if server.CallCount("CreateByokRegistrationKey") != 0 {
		t.Error("an input the API refuses must not reach it")
	}
}

// A repeat after an ambiguous failure would mint a second key, which counts against the account's
// limit until it expires, so a 500 is not retried.
func TestCreateByokRegistrationKeyIsNotRetriedThroughAServerError(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.Error(500, "boom"), testutil.JSON(byokKeyResponse))

	client, err := cloud.New(cloud.Config{
		BaseURL: server.URL, AccountID: testAccountID, ManagementAPIKey: testKey, AllowInsecureHTTP: true,
		Retry: api.RetryPolicy{MaxAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud: cloud.CloudGCP, ProviderRegion: "us-central1"}); err == nil {
		t.Fatal("expected the 500 to surface")
	}
	if got := server.CallCount("CreateByokRegistrationKey"); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestCreateByokRegistrationKeyKeepsTheKeyOutOfErrors(t *testing.T) {
	server := testutil.NewAPIServer(t)
	// A success that names no region is still a credential, and must not be echoed.
	server.On("CreateByokRegistrationKey", testutil.JSON(`{"registrationKey":"rrk_1a2b3c.4d5e6f","expiresAt":"2026-11-18T16:00:00.000Z"}`))

	_, err := newClient(t, server).ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud: cloud.CloudGCP, ProviderRegion: "us-central1"})
	if err == nil || strings.Contains(err.Error(), "4d5e6f") {
		t.Errorf("err = %v, want a refusal that does not carry the key", err)
	}
}

func TestCreateByokRegistrationKeyReportsTheAccountLimit(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateByokRegistrationKey", testutil.Error(429, "too many unused registration keys"))

	_, err := newClient(t, server).ByokRegistrations().Create(context.Background(), cloud.CreateByokRegistrationKeyInput{
		Cloud: cloud.CloudGCP, ProviderRegion: "us-central1"})
	if !api.IsTooManyRequests(err) {
		t.Errorf("err = %v, want a 429", err)
	}
}

func TestDeleteByokRegionTreatsAMissingRegionAsDone(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteByokRegion", testutil.Error(404, "Region rgn_a1b2c3d4e5 not found"))

	if err := newClient(t, server).ByokRegistrations().DeleteRegion(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Fatalf("DeleteRegion: %v", err)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["regionId"] != "rgn_a1b2c3d4e5" {
		t.Errorf("body = %v", body)
	}
}

func TestDeleteByokRegionSurfacesEnvironmentsAsAConflict(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteByokRegion", testutil.Error(409, "The account still has environments in the region"))

	err := newClient(t, server).ByokRegistrations().DeleteRegion(context.Background(), "rgn_a1b2c3d4e5")
	if !api.IsConflict(err) {
		t.Errorf("err = %v, want a 409", err)
	}
}

func TestDeleteByokRegionSucceedsOnADeletedRegion(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteByokRegion", testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","status":"DELETED"}`))

	if err := newClient(t, server).ByokRegistrations().DeleteRegion(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Fatalf("DeleteRegion: %v", err)
	}
}
