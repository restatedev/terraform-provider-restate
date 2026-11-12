package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

const grantJSON = `{
  "grantId":"rgr_x5z2y4qjmt","owner":"ORGANIZATION","ownerId":"org_1","displayName":"aws 123456789012",
  "cloud":"aws","aws":{"accountId":"123456789012"},"status":"PENDING","regionIds":[],
  "createdAt":"2026-11-12T00:00:00.000Z","expiresAt":"2026-11-19T00:00:00.000Z",
  "grantInputs":{"restateIssuer":"https://issuer.restate.cloud","grantSubject":"grant:rgr_x5z2y4qjmt",
    "tokenAudience":"sts.amazonaws.com","resourcePrefix":"rgr-x5z2y4qjmt-"}
}`

// Each cloud names its account under its own key, and an owner or display name the caller left out
// must be absent so the API applies its own default.
func TestCreateRegionGrantSendsTheCloudAccountUnderItsCloud(t *testing.T) {
	tests := []struct {
		name    string
		input   cloud.CreateRegionGrantInput
		wantKey string
		wantID  string
	}{
		{"aws", cloud.CreateRegionGrantInput{Cloud: cloud.CloudAWS, Account: cloud.CloudAccount{AWSAccountID: "123456789012"}}, "aws", "123456789012"},
		{"gcp", cloud.CreateRegionGrantInput{Cloud: cloud.CloudGCP, Account: cloud.CloudAccount{GCPProjectID: "acme-restate"}}, "gcp", "acme-restate"},
		{"azure", cloud.CreateRegionGrantInput{Cloud: cloud.CloudAzure, Account: cloud.CloudAccount{AzureSubscriptionID: "00000000-0000-0000-0000-00000000000A"}}, "azure", "00000000-0000-0000-0000-00000000000a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("CreateRegionGrant", testutil.JSON(grantJSON))

			if _, err := newClient(t, server).RegionGrants().Create(context.Background(), tt.input); err != nil {
				t.Fatalf("Create: %v", err)
			}
			var body map[string]any
			server.LastRequest().DecodeBody(t, &body)
			if body["cloud"] != tt.name {
				t.Errorf("cloud = %v", body["cloud"])
			}
			account, _ := body[tt.wantKey].(map[string]any)
			if got := account["accountId"]; tt.wantKey == "aws" && got != tt.wantID {
				t.Errorf("aws.accountId = %v", got)
			}
			if got := account["projectId"]; tt.wantKey == "gcp" && got != tt.wantID {
				t.Errorf("gcp.projectId = %v", got)
			}
			if got := account["subscriptionId"]; tt.wantKey == "azure" && got != tt.wantID {
				t.Errorf("azure.subscriptionId = %v", got)
			}
			for _, absent := range []string{"owner", "displayName"} {
				if _, present := body[absent]; present {
					t.Errorf("%s was sent despite not being asked for", absent)
				}
			}
		})
	}
}

func TestCreateRegionGrantMapsTheDescription(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateRegionGrant", testutil.JSON(grantJSON))

	got, err := newClient(t, server).RegionGrants().Create(context.Background(), cloud.CreateRegionGrantInput{
		Owner:   cloud.RegionGrantOwnerOrganization,
		Cloud:   cloud.CloudAWS,
		Account: cloud.CloudAccount{AWSAccountID: "123456789012"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["owner"] != "ORGANIZATION" {
		t.Errorf("owner = %v", body["owner"])
	}
	if got.ID != "rgr_x5z2y4qjmt" || got.Owner != cloud.RegionGrantOwnerOrganization || got.OwnerID != "org_1" {
		t.Errorf("identity = %+v", got)
	}
	if got.Account.AWSAccountID != "123456789012" || got.Status != cloud.RegionGrantPending || got.ExpiresAt == "" {
		t.Errorf("account or status = %+v", got)
	}
	if got.Inputs == nil || got.Inputs.GrantSubject != "grant:rgr_x5z2y4qjmt" || got.Inputs.ResourcePrefix != "rgr-x5z2y4qjmt-" {
		t.Errorf("Inputs = %+v", got.Inputs)
	}
}

func TestCreateRegionGrantRejectsAnAzureSubscriptionThatIsNotAUUID(t *testing.T) {
	server := testutil.NewAPIServer(t)
	_, err := newClient(t, server).RegionGrants().Create(context.Background(), cloud.CreateRegionGrantInput{
		Cloud: cloud.CloudAzure, Account: cloud.CloudAccount{AzureSubscriptionID: "not-a-uuid"},
	})
	if err == nil || !strings.Contains(err.Error(), "not a UUID") {
		t.Fatalf("err = %v, want a UUID error", err)
	}
	if server.CallCount("CreateRegionGrant") != 0 {
		t.Error("an invalid request reached the API")
	}
}

func TestGetLiveTreatsEveryEndOfAGrantAsGone(t *testing.T) {
	for _, status := range []string{"EXPIRED", "DELETED"} {
		t.Run(status, func(t *testing.T) {
			server := testutil.NewAPIServer(t)
			server.On("DescribeRegionGrant", testutil.JSON(strings.Replace(grantJSON, `"PENDING"`, `"`+status+`"`, 1)))
			_, err := newClient(t, server).RegionGrants().GetLive(context.Background(), "rgr_x5z2y4qjmt")
			if !errors.Is(err, cloud.ErrRegionGrantGone) {
				t.Errorf("err = %v, want ErrRegionGrantGone", err)
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		server := testutil.NewAPIServer(t)
		server.On("DescribeRegionGrant", testutil.Error(http.StatusNotFound, "no such grant"))
		_, err := newClient(t, server).RegionGrants().GetLive(context.Background(), "rgr_x5z2y4qjmt")
		if !errors.Is(err, cloud.ErrRegionGrantGone) {
			t.Errorf("err = %v, want ErrRegionGrantGone", err)
		}
	})
}

// Waiting cannot fix an invalid output, an ended grant or a conflict, and a timeout must still say
// what the cloud last answered.
func TestWaitUntilConfirmedStopsOnWhatWaitingCannotFix(t *testing.T) {
	output := map[string]string{"roleArn": "r"}

	for _, status := range []int{http.StatusBadRequest, http.StatusGone, http.StatusConflict} {
		server := testutil.NewAPIServer(t)
		server.On("ConfirmRegionGrant", testutil.Error(status, "refused"))
		_, err := newClient(t, server).RegionGrants().WaitUntilConfirmed(context.Background(), "rgr_1", output)
		if apiErr, ok := api.AsError(err); !ok || apiErr.StatusCode != status {
			t.Errorf("status %d: err = %v", status, err)
		}
		if count := server.CallCount("ConfirmRegionGrant"); count != 1 {
			t.Errorf("status %d: ConfirmRegionGrant called %d times, want 1", status, count)
		}
	}

	confirming := strings.Replace(grantJSON, `"status":"PENDING"`,
		`"status":"PENDING","statusReason":"CONFIRMING","statusMessage":"AccessDenied by the role's trust policy"`, 1)
	server := testutil.NewAPIServer(t)
	server.On("ConfirmRegionGrant", testutil.JSON(confirming))
	server.On("DescribeRegionGrant", testutil.JSON(confirming))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := newClient(t, server).RegionGrants().WaitUntilConfirmed(ctx, "rgr_1", output)
	if !errors.Is(err, api.ErrPollTimeout) || !strings.Contains(err.Error(), "trust policy") {
		t.Errorf("err = %v, want a timeout that carries the cloud's last answer", err)
	}
	if count := server.CallCount("ConfirmRegionGrant"); count != 1 {
		t.Errorf("ConfirmRegionGrant called %d times, want 1: the background check is followed, not restarted", count)
	}
}

func TestDeleteRegionGrantTreatsAGoneGrantAsDeletedAndReportsAConflict(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		server := testutil.NewAPIServer(t)
		server.On("DeleteRegionGrant", testutil.Error(status, "gone"))
		if err := newClient(t, server).RegionGrants().Delete(context.Background(), "rgr_1", struct{}{}); err != nil {
			t.Errorf("status %d: Delete = %v, want success", status, err)
		}
	}

	server := testutil.NewAPIServer(t)
	server.On("DeleteRegionGrant", testutil.Error(http.StatusConflict, "a region still uses the grant"))
	if err := newClient(t, server).RegionGrants().Delete(context.Background(), "rgr_1", struct{}{}); !api.IsConflict(err) {
		t.Errorf("Delete = %v, want a conflict", err)
	}
}

func TestListRegionGrantsNamesTheOwner(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("ListRegionGrants", testutil.JSON(`{"grants":[`+grantJSON+`]}`))

	got, err := newClient(t, server).RegionGrants().List(context.Background(), cloud.RegionGrantOwnerOrganization)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "rgr_x5z2y4qjmt" {
		t.Errorf("got %+v", got)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["owner"] != "ORGANIZATION" {
		t.Errorf("owner = %v", body["owner"])
	}
}

// An asynchronous ConfirmRegionGrant accepts the output at once and keeps signing in itself. The
// confirmation is sent once, and the grant is described until it is READY.
func TestWaitUntilConfirmedFollowsAnAsynchronousConfirmation(t *testing.T) {
	confirming := strings.Replace(grantJSON, `"status":"PENDING"`, `"status":"PENDING","statusReason":"CONFIRMING"`, 1)
	server := testutil.NewAPIServer(t)
	server.On("ConfirmRegionGrant", testutil.JSON(confirming))
	server.On("DescribeRegionGrant", testutil.JSON(confirming), testutil.JSON(strings.Replace(grantJSON, `"PENDING"`, `"READY"`, 1)))

	got, err := newClient(t, server).RegionGrants().WaitUntilConfirmed(context.Background(), "rgr_x5z2y4qjmt", map[string]string{"roleArn": "r"})
	if err != nil {
		t.Fatalf("WaitUntilConfirmed: %v", err)
	}
	if got.Status != cloud.RegionGrantReady {
		t.Errorf("Status = %q", got.Status)
	}
	if confirms, describes := server.CallCount("ConfirmRegionGrant"), server.CallCount("DescribeRegionGrant"); confirms != 1 || describes != 2 {
		t.Errorf("ConfirmRegionGrant called %d times and DescribeRegionGrant %d times, want 1 and 2", confirms, describes)
	}
}

// Restate gave up signing in, and waiting longer cannot help: the cloud's answer is the error.
func TestWaitUntilConfirmedReportsAFailedAsynchronousConfirmation(t *testing.T) {
	failed := strings.Replace(grantJSON, `"status":"PENDING"`,
		`"status":"PENDING","statusReason":"CONFIRMATION_FAILED","statusMessage":"AccessDenied by the trust policy"`, 1)
	server := testutil.NewAPIServer(t)
	server.On("ConfirmRegionGrant", testutil.JSON(strings.Replace(failed, "CONFIRMATION_FAILED", "CONFIRMING", 1)))
	server.On("DescribeRegionGrant", testutil.JSON(failed))

	_, err := newClient(t, server).RegionGrants().WaitUntilConfirmed(context.Background(), "rgr_x5z2y4qjmt", map[string]string{"roleArn": "r"})
	if err == nil || !strings.Contains(err.Error(), "AccessDenied by the trust policy") {
		t.Errorf("err = %v, want the cloud's answer", err)
	}
}
