package client

import (
	"context"
	"github.com/ikaevus/routegate/agent/internal/tasks"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeldRemovalClaimRequiresExactStatusAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"empty", `{}`, 200, true},
		{"recovery challenge", `{"recoveryChallenge":{}}`, 200, false},
		{"unexpected success", `{}`, 202, false},
		{"missing deadline", `{"task":{"id":"untrusted"}}`, 200, false},
		{"expired", `{"task":{"deliveryExpiresAt":"2000-01-01T00:00:00Z"}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != heldRemovalPath+"claim" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("unexpected exchange request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			report, err := New(srv.URL).ProcessHeldRemoval(context.Background(), "fixture", tasks.CredentialRemovalExecutor{})
			if (err == nil) != tc.ok || report != nil || calls != 1 {
				t.Fatalf("claim result: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestHeldRemovalAcknowledgementRequires204(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := New(srv.URL).doJSONExpect(context.Background(), http.MethodPost, heldRemovalPath+"fixture/result", "fixture", nil, nil, http.StatusNoContent)
		srv.Close()
		if (err == nil) != (status == 204) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}
