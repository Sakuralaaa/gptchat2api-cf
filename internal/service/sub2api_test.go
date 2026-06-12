package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSub2APIListRemoteGroupsUsesActiveOpenAIGroupsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/groups/all" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("platform") != "openai" {
			t.Fatalf("platform query = %q, want openai", r.URL.Query().Get("platform"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":[{"id":12,"name":"Team","description":"Main","platform":"openai","status":"active","account_count":3,"active_account_count":2}]}`))
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	groups, err := service.ListRemoteGroups(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	})
	if err != nil {
		t.Fatalf("ListRemoteGroups() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("ListRemoteGroups() length = %d, want 1: %#v", len(groups), groups)
	}
	if groups[0]["id"] != "12" || groups[0]["name"] != "Team" {
		t.Fatalf("ListRemoteGroups() group = %#v", groups[0])
	}
}

func TestSub2APIListRemoteGroupsReturnsEmptyArrayForNullItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/groups/all" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	groups, err := service.ListRemoteGroups(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	})
	if err != nil {
		t.Fatalf("ListRemoteGroups() error = %v", err)
	}
	if groups == nil {
		t.Fatal("ListRemoteGroups() = nil, want empty slice")
	}
	if len(groups) != 0 {
		t.Fatalf("ListRemoteGroups() length = %d, want 0", len(groups))
	}
}

func TestSub2APIListRemoteAccountsKeepsRedactedAccountsWithAccessTokenStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("platform") != "openai" || r.URL.Query().Get("type") != "oauth" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"items":[{"id":123,"name":"user@example.com","status":"active","credentials":{"email":"user@example.com","plan_type":"Plus"},"credentials_status":{"has_access_token":true,"has_refresh_token":true}}],"total":1}}`))
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	accounts, err := service.ListRemoteAccounts(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	})
	if err != nil {
		t.Fatalf("ListRemoteAccounts() error = %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("ListRemoteAccounts() length = %d, want 1: %#v", len(accounts), accounts)
	}
	if accounts[0]["id"] != "123" || accounts[0]["email"] != "user@example.com" || accounts[0]["plan_type"] != "Plus" {
		t.Fatalf("ListRemoteAccounts() account = %#v", accounts[0])
	}
	if accounts[0]["has_refresh_token"] != true {
		t.Fatalf("has_refresh_token = %#v, want true", accounts[0]["has_refresh_token"])
	}
}

func TestSub2APIListRemoteAccountsSkipsRedactedAccountsWithoutAccessTokenStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"items":[{"id":123,"name":"missing-token","credentials":{"email":"missing@example.com"},"credentials_status":{"has_refresh_token":true}}],"total":1}}`))
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	accounts, err := service.ListRemoteAccounts(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	})
	if err != nil {
		t.Fatalf("ListRemoteAccounts() error = %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("ListRemoteAccounts() length = %d, want 0: %#v", len(accounts), accounts)
	}
}

func TestSub2APIFetchAccessTokenPrefersDataExportAndIgnoresRefreshToken(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/v1/admin/accounts/data" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("ids") != "123" || r.URL.Query().Get("include_proxies") != "false" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"accounts":[{"name":"user@example.com","credentials":{"access_token":"access-token-from-export","refresh_token":"refresh-token-must-not-be-imported","id_token":"id-token-must-not-be-imported"}}]}}`))
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	token, err := service.fetchAccessTokenForAccount(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	}, "123")
	if err != nil {
		t.Fatalf("fetchAccessTokenForAccount() error = %v", err)
	}
	if token != "access-token-from-export" {
		t.Fatalf("token = %q, want access-token-from-export", token)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/admin/accounts/data"}) {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestSub2APIFetchAccessTokenFallsBackToLegacyDetailWhenDataExportMissing(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/accounts/data":
			http.NotFound(w, r)
		case "/api/v1/admin/accounts/123":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"credentials":{"access_token":"legacy-access-token","refresh_token":"legacy-refresh-token-must-not-be-imported"}}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	service := NewSub2APIService(NewSub2APIConfig(newTestStorageBackend(t)), nil)
	token, err := service.fetchAccessTokenForAccount(context.Background(), map[string]any{
		"base_url": server.URL,
		"api_key":  "test-key",
	}, "123")
	if err != nil {
		t.Fatalf("fetchAccessTokenForAccount() error = %v", err)
	}
	if token != "legacy-access-token" {
		t.Fatalf("token = %q, want legacy-access-token", token)
	}
	wantPaths := []string{"/api/v1/admin/accounts/data", "/api/v1/admin/accounts/123"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}
