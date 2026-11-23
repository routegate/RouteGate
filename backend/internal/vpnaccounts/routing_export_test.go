package vpnaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type routingExportFixture struct {
	accountRepository
	profile *RoutingProfile
}

func (f routingExportFixture) GetRoutingPolicy(context.Context, string) (VPNAccountRoutingPolicy, error) {
	return VPNAccountRoutingPolicy{EffectiveRoutingProfile: &RoutingProfilePolicySummary{ID: f.profile.ID}}, nil
}

func (f routingExportFixture) listRoutingProfileRules(_ context.Context, profileID string) ([]RoutingProfileRule, error) {
	if profileID != f.profile.ID {
		return nil, nil
	}
	return f.profile.Rules, nil
}

func TestExportV2RayNRoutingMatchesSubscriptionRules(t *testing.T) {
	profile := rg115bTestRoutingProfile()
	handler := Handler{accounts: routingExportFixture{profile: profile}}
	response := httptest.NewRecorder()
	handler.ExportV2RayNRouting(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected export response: %d %s", response.Code, response.Body.String())
	}
	var got []v2RayNCustomRoutingRule
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	payload, err := renderSubscriptionDeliveryPayload(ClientConnectionResponse{}, SubscriptionProfile{RoutingProfile: profile}, SubscriptionDeliveryFormatV2RayNRouting)
	if err != nil {
		t.Fatal(err)
	}
	var want []v2RayNCustomRoutingRule
	if err := json.Unmarshal([]byte(payload.Body), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operator export differs from the subscription export: %+v", got)
	}
}
