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

func regionJSON(status string) string {
	return `{"regionId":"rgn_a1b2c3d4e5","name":"prod-eu","displayName":"prod-eu","setup":"GRANT","cloud":"gcp",
	  "cloudRegion":"europe-west1","gcp":{"projectId":"acme-restate"},"infrastructure":{"machineType":"e2-standard-4"},
	  "grantId":"rgr_x5z2y4qjmt","status":"` + status + `","statusReason":"DEPLOYING","createdAt":"2026-11-12T00:00:00.000Z"}`
}

func TestCreateByocRegionSendsAGrantRegionRequest(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("CreateRegion", testutil.JSON(regionJSON("PENDING")))

	got, err := newClient(t, server).ByocRegions().Create(context.Background(), cloud.CreateRegionInput{
		Name:        "prod-eu",
		GrantID:     "rgr_x5z2y4qjmt",
		Cloud:       cloud.CloudGCP,
		CloudRegion: "europe-west1",
		Account:     cloud.CloudAccount{GCPProjectID: "acme-restate"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "rgn_a1b2c3d4e5" || got.GrantID != "rgr_x5z2y4qjmt" || got.Account.GCPProjectID != "acme-restate" ||
		got.MachineType != "e2-standard-4" || got.Status != cloud.RegionPending || got.StatusReason != "DEPLOYING" {
		t.Errorf("got %+v", got)
	}

	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["setup"] != "GRANT" || body["grantId"] != "rgr_x5z2y4qjmt" || body["cloud"] != "gcp" || body["cloudRegion"] != "europe-west1" {
		t.Errorf("body = %v", body)
	}
	if project, _ := body["gcp"].(map[string]any); project["projectId"] != "acme-restate" {
		t.Errorf("gcp = %v", body["gcp"])
	}
	// The grant decides the identity, and Restate the defaults, so none of these may be sent unasked.
	for _, absent := range []string{"identity", "displayName", "infrastructure", "aws", "azure"} {
		if _, present := body[absent]; present {
			t.Errorf("%s was sent despite not being asked for", absent)
		}
	}
}

func TestWaitUntilActiveReportsAFailedBuildWithItsReason(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeRegion",
		testutil.JSON(regionJSON("PENDING")),
		testutil.JSON(strings.Replace(regionJSON("FAILED"), "DEPLOYING", "REJECTED", 1)),
	)

	_, err := newClient(t, server).ByocRegions().WaitUntilActive(context.Background(), "rgn_a1b2c3d4e5")
	if err == nil || !strings.Contains(err.Error(), "rgn_a1b2c3d4e5") || !strings.Contains(err.Error(), "REJECTED") {
		t.Errorf("err = %v, want the region and its reason", err)
	}
}

// DeleteRegion answers 409 while the teardown runs, so the facade asks again until it answers the
// description of a deleted region.
func TestDeleteAndWaitRepeatsTheDeleteWhileTheTeardownRuns(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteRegion",
		testutil.Error(http.StatusConflict, "rgn_a1b2c3d4e5 is being deleted"),
		testutil.Error(http.StatusConflict, "rgn_a1b2c3d4e5 is being deleted"),
		testutil.JSON(regionJSON("DELETED")),
	)
	server.On("DescribeRegion", testutil.JSON(regionJSON("DELETING")), testutil.JSON(regionJSON("DELETING")))

	if err := newClient(t, server).ByocRegions().DeleteAndWait(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Fatalf("DeleteAndWait: %v", err)
	}
	if count := server.CallCount("DeleteRegion"); count != 3 {
		t.Errorf("DeleteRegion called %d times, want 3", count)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if body["regionId"] != "rgn_a1b2c3d4e5" {
		t.Errorf("body = %v", body)
	}
}

// A 409 from a region that is not being deleted is a refusal, for example because it still has
// environments, and waiting would only hide it.
func TestDeleteAndWaitReportsARefusal(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DeleteRegion", testutil.Error(http.StatusConflict, "the region still has environments"))
	server.On("DescribeRegion", testutil.JSON(regionJSON("ACTIVE")))

	err := newClient(t, server).ByocRegions().DeleteAndWait(context.Background(), "rgn_a1b2c3d4e5")
	if !api.IsConflict(err) || !strings.Contains(err.Error(), "environments") {
		t.Errorf("err = %v, want the API's refusal", err)
	}
	if count := server.CallCount("DeleteRegion"); count != 1 {
		t.Errorf("DeleteRegion called %d times, want 1", count)
	}
}

func TestByocRegionGoneByEitherForm(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeRegion", testutil.Error(http.StatusNotFound, "no such region"), testutil.JSON(regionJSON("DELETED")))
	client := newClient(t, server)

	for range 2 {
		if _, err := client.ByocRegions().GetExisting(context.Background(), "rgn_a1b2c3d4e5"); !errors.Is(err, cloud.ErrRegionGone) {
			t.Errorf("err = %v, want ErrRegionGone", err)
		}
	}

	deleteServer := testutil.NewAPIServer(t)
	deleteServer.On("DeleteRegion", testutil.Error(http.StatusNotFound, "no such region"))
	if err := newClient(t, deleteServer).ByocRegions().DeleteAndWait(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Errorf("DeleteAndWait on a missing region = %v, want success", err)
	}
}
