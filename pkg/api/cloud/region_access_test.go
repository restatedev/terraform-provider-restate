package cloud_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restatedev/terraform-provider-restate/internal/testutil"
	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud"
)

const awsRegionWithAccess = `{"regionId":"rgn_a1b2c3d4e5","name":"prod-eu","displayName":"prod-eu","setup":"GRANT","cloud":"aws",
  "cloudRegion":"eu-central-1","aws":{"accountId":"123456789012"},"infrastructure":{},"grantId":"rgr_x5z2y4qjmt",
  "status":"ACTIVE","createdAt":"2026-11-12T00:00:00.000Z","access":%s}`

func awsRegion(access string) string { return strings.Replace(awsRegionWithAccess, "%s", access, 1) }

func TestSetAccessSendsTheWholeSetting(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess", testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","dryRun":false,"entrySourceCidrs":[],
	  "privateLink":{"enabled":true,"allowedPrincipals":["arn:aws:iam::210987654321:root"],"acceptanceRequired":true}}`))

	stored, err := newClient(t, server).ByocRegions().SetAccess(context.Background(), "rgn_a1b2c3d4e5", cloud.RegionAccess{
		EntrySourceCIDRs: []string{},
		PrivateLink: &cloud.RegionPrivateLink{
			Enabled:            true,
			AllowedPrincipals:  []string{"arn:aws:iam::210987654321:root"},
			AcceptanceRequired: true,
		},
	})
	if err != nil {
		t.Fatalf("SetAccess: %v", err)
	}

	request := server.LastRequest()
	if request.Path != "/"+testAccountID+"/SetRegionAccess" || request.Authorization != "Bearer "+testKey {
		t.Errorf("request = %s, authorization %q", request.Path, request.Authorization)
	}
	var body map[string]any
	request.DecodeBody(t, &body)
	want := map[string]any{
		"regionId":         "rgn_a1b2c3d4e5",
		"entrySourceCidrs": []any{},
		"privateLink": map[string]any{
			"enabled":            true,
			"allowedPrincipals":  []any{"arn:aws:iam::210987654321:root"},
			"acceptanceRequired": true,
		},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	// An empty list admits no client directly, so it must not collapse into an absent one.
	if stored.EntrySourceCIDRs == nil || len(stored.EntrySourceCIDRs) != 0 {
		t.Errorf("EntrySourceCIDRs = %#v, want an empty, non-nil list", stored.EntrySourceCIDRs)
	}
	if stored.PrivateLink == nil || !stored.PrivateLink.Enabled || !stored.PrivateLink.AcceptanceRequired ||
		!reflect.DeepEqual(stored.PrivateLink.AllowedPrincipals, []string{"arn:aws:iam::210987654321:root"}) {
		t.Errorf("PrivateLink = %+v", stored.PrivateLink)
	}
}

// The call replaces the whole setting, so a field the caller left out must be absent from the
// request, where the API reads it as the default, and never a zero value sent in its place.
func TestSetAccessOmitsWhatWasNotSet(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess", testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","dryRun":false}`))

	stored, err := newClient(t, server).ByocRegions().SetAccess(context.Background(), "rgn_a1b2c3d4e5", cloud.RegionAccess{})
	if err != nil {
		t.Fatalf("SetAccess: %v", err)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if !reflect.DeepEqual(body, map[string]any{"regionId": "rgn_a1b2c3d4e5"}) {
		t.Errorf("body = %v, want only the region ID", body)
	}
	if stored.EntrySourceCIDRs != nil || stored.PrivateLink != nil {
		t.Errorf("stored = %+v, want the defaults", stored)
	}
}

func TestSetAccessSendsNoPrincipalListWhenThereAreNone(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess", testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","dryRun":false,"privateLink":{"enabled":true}}`))

	_, err := newClient(t, server).ByocRegions().SetAccess(context.Background(), "rgn_a1b2c3d4e5",
		cloud.RegionAccess{PrivateLink: &cloud.RegionPrivateLink{Enabled: true}})
	if err != nil {
		t.Fatalf("SetAccess: %v", err)
	}
	var body struct {
		PrivateLink map[string]any `json:"privateLink"`
	}
	server.LastRequest().DecodeBody(t, &body)
	if _, present := body.PrivateLink["allowedPrincipals"]; present {
		t.Errorf("privateLink = %v, want no allowedPrincipals", body.PrivateLink)
	}
	if body.PrivateLink["acceptanceRequired"] != false {
		t.Errorf("privateLink = %v, want acceptanceRequired sent explicitly as false", body.PrivateLink)
	}
}

func TestSetAccessRequiresARegion(t *testing.T) {
	if _, err := newClient(t, testutil.NewAPIServer(t)).ByocRegions().SetAccess(context.Background(), "", cloud.RegionAccess{}); err == nil {
		t.Error("SetAccess without a region ID succeeded")
	}
}

func TestSetAccessReportsEachRefusalWithItsStatus(t *testing.T) {
	for status, check := range map[int]func(error) bool{
		http.StatusBadRequest:     api.IsBadRequest,
		http.StatusForbidden:      api.IsForbidden,
		http.StatusNotFound:       api.IsNotFound,
		http.StatusConflict:       api.IsConflict,
		http.StatusNotImplemented: api.IsNotImplemented,
	} {
		server := testutil.NewAPIServer(t)
		server.On("SetRegionAccess", testutil.Error(status, "refused for a reason"))
		_, err := newClient(t, server).ByocRegions().SetAccess(context.Background(), "rgn_a1b2c3d4e5", cloud.RegionAccess{})
		if !check(err) || !strings.Contains(err.Error(), "refused for a reason") {
			t.Errorf("status %d: err = %v, want the status and the API's message", status, err)
		}
	}
}

func TestGetMapsTheAccessOfARegion(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeRegion",
		testutil.JSON(awsRegion(`{"entrySourceCidrs":["203.0.113.0/24"],"privateLink":{"enabled":true,
		  "allowedPrincipals":["arn:aws:iam::210987654321:root"],"acceptanceRequired":false,
		  "endpointService":{"serviceName":"com.amazonaws.vpce.eu-central-1.vpce-svc-0a1b","state":"Available",
		  "availabilityZoneIds":["euc1-az1","euc1-az2"]}}}`)),
		testutil.JSON(awsRegion(`{"entrySourceCidrs":[]}`)),
		testutil.JSON(awsRegion(`{}`)),
	)
	regions := newClient(t, server).ByocRegions()

	region, err := regions.Get(context.Background(), "rgn_a1b2c3d4e5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := &cloud.RegionAccess{
		EntrySourceCIDRs: []string{"203.0.113.0/24"},
		PrivateLink: &cloud.RegionPrivateLink{
			Enabled:           true,
			AllowedPrincipals: []string{"arn:aws:iam::210987654321:root"},
			EndpointService: &cloud.RegionEndpointService{
				ServiceName:         "com.amazonaws.vpce.eu-central-1.vpce-svc-0a1b",
				State:               "Available",
				AvailabilityZoneIDs: []string{"euc1-az1", "euc1-az2"},
			},
		},
	}
	if !reflect.DeepEqual(region.Access, want) {
		t.Errorf("Access = %+v, want %+v", region.Access, want)
	}

	region, _ = regions.Get(context.Background(), "rgn_a1b2c3d4e5")
	if region.Access == nil || region.Access.EntrySourceCIDRs == nil || len(region.Access.EntrySourceCIDRs) != 0 {
		t.Errorf("Access = %+v, want an empty, non-nil list: no address is admitted", region.Access)
	}

	region, _ = regions.Get(context.Background(), "rgn_a1b2c3d4e5")
	if region.Access == nil || region.Access.EntrySourceCIDRs != nil || region.Access.PrivateLink != nil {
		t.Errorf("Access = %+v, want an empty setting: every address is admitted", region.Access)
	}
}

func TestGetReportsNoAccessWhenNobodySetIt(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("DescribeRegion", testutil.JSON(regionJSON("ACTIVE")))
	region, err := newClient(t, server).ByocRegions().Get(context.Background(), "rgn_a1b2c3d4e5")
	if err != nil || region.Access != nil {
		t.Errorf("Get = %+v, %v, want no access", region.Access, err)
	}
}

func TestSetAccessWhenReadyWaitsThroughAnUnbuiltRegionAndAnUnavailableService(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess",
		testutil.Error(http.StatusConflict, "the region is not built yet"),
		testutil.Error(http.StatusServiceUnavailable, "the region's infrastructure cannot take the setting yet"),
		testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","dryRun":false,"entrySourceCidrs":["203.0.113.0/24"]}`),
	)
	server.On("DescribeRegion", testutil.JSON(regionJSON("PENDING")))

	stored, err := newClient(t, server).ByocRegions().SetAccessWhenReady(context.Background(), "rgn_a1b2c3d4e5",
		cloud.RegionAccess{EntrySourceCIDRs: []string{"203.0.113.0/24"}})
	if err != nil {
		t.Fatalf("SetAccessWhenReady: %v", err)
	}
	if !reflect.DeepEqual(stored.EntrySourceCIDRs, []string{"203.0.113.0/24"}) {
		t.Errorf("stored = %+v", stored)
	}
	if count := server.CallCount("SetRegionAccess"); count != 3 {
		t.Errorf("SetRegionAccess called %d times, want 3", count)
	}
}

// A 409 for a region that is built means it cannot take the setting at all, for example because it
// was set up with an installer, and waiting would never change that.
func TestSetAccessWhenReadyStopsOnARefusalForABuiltRegion(t *testing.T) {
	for name, status := range map[string]string{"ACTIVE": "ACTIVE", "DELETING": "DELETING", "FAILED": "FAILED"} {
		server := testutil.NewAPIServer(t)
		server.On("SetRegionAccess", testutil.Error(http.StatusConflict, "the region is set up with an installer"))
		server.On("DescribeRegion", testutil.JSON(regionJSON(status)))

		_, err := newClient(t, server).ByocRegions().SetAccessWhenReady(context.Background(), "rgn_a1b2c3d4e5", cloud.RegionAccess{})
		if !api.IsConflict(err) || !strings.Contains(err.Error(), "installer") {
			t.Errorf("%s: err = %v, want the API's refusal", name, err)
		}
		if count := server.CallCount("SetRegionAccess"); count != 1 {
			t.Errorf("%s: SetRegionAccess called %d times, want 1", name, count)
		}
	}
}

func TestSetAccessWhenReadyDoesNotRetryAnInvalidSetting(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusNotImplemented} {
		server := testutil.NewAPIServer(t)
		server.On("SetRegionAccess", testutil.Error(status, "refused"))
		_, err := newClient(t, server).ByocRegions().SetAccessWhenReady(context.Background(), "rgn_a1b2c3d4e5", cloud.RegionAccess{})
		if err == nil || server.CallCount("SetRegionAccess") != 1 {
			t.Errorf("status %d: err = %v after %d calls, want one refused call", status, err, server.CallCount("SetRegionAccess"))
		}
	}
}

func TestSetAccessWhenReadyReportsTheLastAnswerWhenItTimesOut(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess", testutil.Error(http.StatusServiceUnavailable, "the region's infrastructure cannot take the setting yet"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := newClient(t, server).ByocRegions().SetAccessWhenReady(ctx, "rgn_a1b2c3d4e5", cloud.RegionAccess{})
	if err == nil || !strings.Contains(err.Error(), "cannot take the setting yet") {
		t.Errorf("err = %v, want the last answer", err)
	}
}

func TestResetAccessSendsAnEmptySettingAndAcceptsAGoneRegion(t *testing.T) {
	server := testutil.NewAPIServer(t)
	server.On("SetRegionAccess", testutil.JSON(`{"regionId":"rgn_a1b2c3d4e5","dryRun":false}`))
	if err := newClient(t, server).ByocRegions().ResetAccess(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Fatalf("ResetAccess: %v", err)
	}
	var body map[string]any
	server.LastRequest().DecodeBody(t, &body)
	if !reflect.DeepEqual(body, map[string]any{"regionId": "rgn_a1b2c3d4e5"}) {
		t.Errorf("body = %v, want only the region ID", body)
	}

	missing := testutil.NewAPIServer(t)
	missing.On("SetRegionAccess", testutil.Error(http.StatusNotFound, "no such region"))
	if err := newClient(t, missing).ByocRegions().ResetAccess(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Errorf("ResetAccess on a missing region = %v, want success", err)
	}

	deleting := testutil.NewAPIServer(t)
	deleting.On("SetRegionAccess", testutil.Error(http.StatusConflict, "the region is being deleted"))
	deleting.On("DescribeRegion", testutil.JSON(regionJSON("DELETING")))
	if err := newClient(t, deleting).ByocRegions().ResetAccess(context.Background(), "rgn_a1b2c3d4e5"); err != nil {
		t.Errorf("ResetAccess on a region being deleted = %v, want success", err)
	}

	refused := testutil.NewAPIServer(t)
	refused.On("SetRegionAccess", testutil.Error(http.StatusForbidden, "the grant may not be used"))
	if err := newClient(t, refused).ByocRegions().ResetAccess(context.Background(), "rgn_a1b2c3d4e5"); !api.IsForbidden(err) {
		t.Errorf("ResetAccess = %v, want the 403", err)
	}
}

func TestWaitForEndpointService(t *testing.T) {
	enabled := `{"privateLink":{"enabled":true%s}}`
	server := testutil.NewAPIServer(t)
	server.On("DescribeRegion",
		testutil.JSON(awsRegion(strings.Replace(enabled, "%s", "", 1))),
		testutil.JSON(awsRegion(strings.Replace(enabled, "%s", `,"endpointService":{"state":"Pending"}`, 1))),
		testutil.JSON(awsRegion(strings.Replace(enabled, "%s", `,"endpointService":{"serviceName":"com.amazonaws.vpce.eu-central-1.vpce-svc-0a1b","state":"Available"}`, 1))),
	)
	region, err := newClient(t, server).ByocRegions().WaitForEndpointService(context.Background(), "rgn_a1b2c3d4e5")
	if err != nil {
		t.Fatalf("WaitForEndpointService: %v", err)
	}
	if got := region.Access.PrivateLink.EndpointService.ServiceName; got != "com.amazonaws.vpce.eu-central-1.vpce-svc-0a1b" {
		t.Errorf("service name = %q", got)
	}
	if count := server.CallCount("DescribeRegion"); count != 3 {
		t.Errorf("DescribeRegion called %d times, want 3", count)
	}

	// A region that no longer has PrivateLink will never get a service, so waiting would only run out
	// the timeout.
	disabled := testutil.NewAPIServer(t)
	disabled.On("DescribeRegion", testutil.JSON(awsRegion(`{}`)))
	if _, err := newClient(t, disabled).ByocRegions().WaitForEndpointService(context.Background(), "rgn_a1b2c3d4e5"); err == nil ||
		!strings.Contains(err.Error(), "no enabled PrivateLink") {
		t.Errorf("err = %v, want the missing PrivateLink", err)
	}
}
