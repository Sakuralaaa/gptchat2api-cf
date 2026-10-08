package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt2api/internal/util"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// makeJWT builds a minimal unsigned-JWT-shaped token with the given exp.
func makeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{"exp": exp.Unix()})
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// stubRefresher replaces the account service refresher with one that always
// returns the given tokens, and reports how many times it was called.
func stubRefresher(t *testing.T, s *AccountService, newAT, newST string, err error) *int32 {
	t.Helper()
	var calls int32
	s.refresher = NewSessionRefresher(func(req *http.Request) (*http.Response, error) {
		calls++
		if err != nil {
			return nil, err
		}
		body := fmt.Sprintf(`{"accessToken":%q,"sessionToken":%q,"expires":"%s"}`,
			newAT, newST, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	return &calls
}

// ── 1. AddRegisteredAccount ─────────────────────────────────────────────────

func TestAddRegisteredAccountPersistsSessionAndCredentials(t *testing.T) {
	accounts := newTestAccountService(t)
	accounts.AddRegisteredAccount(map[string]any{
		"email":         "user@example.test",
		"password":      "P@ssw0rd-123",
		"access_token":  "at-1",
		"session_token": "st-1",
		"mail_provider": "cloudflare_temp_email",
		"mail_ref":      "cloudflare_temp_email#1",
		"mail_token":    "jwt-token",
		"created_at":    "2026-01-01T00:00:00Z",
	})
	account := accounts.GetAccount("at-1")
	if account == nil {
		t.Fatal("registered account missing from pool")
	}
	for key, want := range map[string]string{
		"session_token": "st-1",
		"password":      "P@ssw0rd-123",
		"email":         "user@example.test",
		"mail_provider": "cloudflare_temp_email",
		"mail_ref":      "cloudflare_temp_email#1",
		"mail_token":    "jwt-token",
	} {
		if got := util.Clean(account[key]); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	// Credentials must not leak through the public account view.
	for _, public := range accounts.ListAccounts() {
		if _, ok := public["password"]; ok {
			t.Fatal("public account leaks password")
		}
		if _, ok := public["mail_token"]; ok {
			t.Fatal("public account leaks mail_token")
		}
	}
}

// ── 2. Refresh failure backoff ──────────────────────────────────────────────

func TestRefreshFailureBackoffSchedulesRetry(t *testing.T) {
	accounts := newTestAccountService(t)
	accounts.AddAccounts([]string{"at-1"})
	accounts.UpdateAccount("at-1", map[string]any{"session_token": "st-1"})

	stubRefresher(t, accounts, "", "", fmt.Errorf("session endpoint returned 502: bad gateway"))

	// Backoff table sanity: 1min → 5min → 30min → 2h.
	if refreshBackoffFor(1) != time.Minute || refreshBackoffFor(2) != 5*time.Minute ||
		refreshBackoffFor(3) != 30*time.Minute || refreshBackoffFor(4) != 2*time.Hour {
		t.Fatalf("refreshBackoffFor table mismatch: %v", refreshBackoffDelays)
	}

	var lastNext time.Time
	for i := 1; i <= maxRefreshFailures; i++ {
		accounts.applyRefreshFailure("at-1", "session endpoint returned 502: bad gateway")
		account := accounts.GetAccount("at-1")
		if got := util.ToInt(account["refresh_failures"], 0); got != i {
			t.Fatalf("refresh_failures = %d, want %d", got, i)
		}
		if i < maxRefreshFailures {
			if got := util.Clean(account["status"]); got != "过期待刷新" {
				t.Fatalf("status after %d failures = %q, want 过期待刷新", i, got)
			}
			next, ok := parseAccountRestoreAt(account["refresh_next_at"])
			if !ok {
				t.Fatalf("refresh_next_at missing after %d failures", i)
			}
			if !next.After(lastNext) {
				t.Fatalf("backoff not increasing: %v after %d", next, i)
			}
			lastNext = next
		} else if got := util.Clean(account["status"]); got != "异常" {
			t.Fatalf("status after %d failures = %q, want 异常", i, got)
		}
	}
	// Escalated account must remain in the pool for password relogin.
	if accounts.GetAccount("at-1") == nil {
		t.Fatal("account removed after reaching max refresh failures")
	}
}

// ── 3. Session refresh watcher ──────────────────────────────────────────────

func TestSessionRefreshWatcherRefreshesExpiringToken(t *testing.T) {
	accounts := newTestAccountService(t)
	expiringAT := makeJWT(t, time.Now().Add(10*time.Minute))
	freshAT := makeJWT(t, time.Now().Add(12*time.Hour))
	accounts.AddAccounts([]string{expiringAT})
	accounts.UpdateAccount(expiringAT, map[string]any{"session_token": "st-1"})

	calls := stubRefresher(t, accounts, freshAT, "st-2", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accounts.StartSessionRefreshWatcher(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && *calls == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if *calls == 0 {
		t.Fatal("session refresh watcher never refreshed the expiring token")
	}
	if accounts.GetAccount(freshAT) == nil {
		t.Fatal("refreshed access token not rotated into the pool")
	}
	if got := util.Clean(accounts.GetAccount(freshAT)["session_token"]); got != "st-2" {
		t.Fatalf("session_token = %q, want st-2", got)
	}
}

func TestSessionRefreshWatcherSkipsLongLivedToken(t *testing.T) {
	accounts := newTestAccountService(t)
	longLivedAT := makeJWT(t, time.Now().Add(12*time.Hour))
	accounts.AddAccounts([]string{longLivedAT})
	accounts.UpdateAccount(longLivedAT, map[string]any{"session_token": "st-1"})

	calls := stubRefresher(t, accounts, makeJWT(t, time.Now().Add(time.Hour)), "st-2", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accounts.StartSessionRefreshWatcher(ctx, 20*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("watcher refreshed a token that is still valid for hours: calls = %d", *calls)
	}
}

// ── 4. Password relogin with OTP ────────────────────────────────────────────

// fakeMailServer pretends to be an inbucket-compatible mail API serving one
// OTP message for the target address (mailbox name derived from the local
// part "user").
func fakeMailServer(t *testing.T, code string) *httptest.Server {
	t.Helper()
	detail := map[string]any{
		"subject": "Verify your email",
		"from":    "noreply@openai.com",
		"body": map[string]any{
			"text": "Your verification code is " + code,
			"html": "<p>" + code + "</p>",
		},
		"date": time.Now().UTC().Format(time.RFC3339),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/mailbox/user" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "msg-1", "subject": detail["subject"], "from": detail["from"], "date": detail["date"]},
			})
		case r.URL.Path == "/api/v1/mailbox/user/msg-1" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(detail)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReloginSubmitsPasswordAndHandlesOTP(t *testing.T) {
	var mu sync.Mutex
	var sequence []string
	otpChallenged := false
	var serverURL string

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		sequence = append(sequence, req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/api/auth/csrf":
			return registerJSONResponse(req, http.StatusOK, `{"csrfToken":"csrf-1"}`), nil
		case "/api/auth/signin/openai":
			return registerJSONResponse(req, http.StatusOK,
				`{"url":"`+serverURL+`/authorize?state=s1"}`), nil
		case "/backend-api/sentinel/req":
			return registerJSONResponse(req, http.StatusOK,
				`{"token":"challenge-token","proofofwork":{"required":false}}`), nil
		case "/api/accounts/authorize/continue":
			return registerJSONResponse(req, http.StatusOK, `{}`), nil
		case "/api/accounts/password/verify":
			if !otpChallenged {
				otpChallenged = true
				return registerJSONResponse(req, http.StatusOK,
					`{"page":{"type":"email_otp_verification"},"continue_url":"/email-verification"}`), nil
			}
			return registerJSONResponse(req, http.StatusOK,
				`{"continue_url":"`+serverURL+`/authorize/resume"}`), nil
		case "/api/accounts/email-otp/validate":
			return registerJSONResponse(req, http.StatusOK,
				`{"continue_url":"`+serverURL+`/authorize/resume"}`), nil
		case "/authorize/resume":
			resp := registerJSONResponse(req, http.StatusFound, `{}`)
			resp.Header.Set("Location", serverURL+"/api/auth/callback/openai?code=cb-code&state=s1")
			return resp, nil
		case "/api/auth/session":
			return registerJSONResponse(req, http.StatusOK, `{"accessToken":"new-access-token"}`), nil
		default:
			return registerJSONResponse(req, http.StatusOK, `{}`), nil
		}
	})

	// Pre-seed the jar so warmup() and getAuthSession() succeed without a real
	// Cloudflare round trip (the stub transport never plants cookies itself).
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() error = %v", err)
	}
	chatURL, _ := url.Parse("https://chatgpt.com")
	authURL, _ := url.Parse("https://auth.openai.com")
	jar.SetCookies(chatURL, []*http.Cookie{
		{Name: "oai-did", Value: "device-1", Path: "/"},
		{Name: "__Secure-next-auth.session-token", Value: "new-session-token", Path: "/"},
	})
	jar.SetCookies(authURL, []*http.Cookie{{Name: "oai-did", Value: "device-1", Path: "/"}})

	mail := fakeMailServer(t, "654321")

	worker := &registerWorker{
		service:  &RegisterService{},
		index:    0,
		deviceID: "device-1",
		mail: map[string]any{
			"request_timeout": 2,
			"wait_timeout":    2,
			"wait_interval":   1,
			"providers": []any{
				map[string]any{
					"type":         "inbucket",
					"enable":       true,
					"api_base":     mail.URL,
					"domain":       []string{"mail.example.test"},
					"provider_ref": "inbucket#1",
				},
			},
		},
		client: &http.Client{Jar: jar, Transport: transport},
	}
	creds := map[string]any{
		"email":         "user@mail.example.test",
		"password":      "P@ssw0rd-123",
		"mail_provider": "inbucket",
		"mail_ref":      "inbucket#1",
		"mail_token":    "",
	}

	serverURL = "https://auth.openai.test"
	newAT, newST, err := worker.reloginSession(context.Background(), "user@mail.example.test", "P@ssw0rd-123", creds)
	if err != nil {
		t.Fatalf("reloginSession() error = %v", err)
	}
	if newAT != "new-access-token" {
		t.Fatalf("new access token = %q", newAT)
	}
	if newST != "new-session-token" {
		t.Fatalf("new session token = %q", newST)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(sequence, ",")
	for _, want := range []string{
		"/api/auth/csrf",
		"/api/accounts/authorize/continue",
		"/api/accounts/password/verify",
		"/api/accounts/email-otp/validate",
		"/api/auth/session",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sequence missing %s: %#v", want, sequence)
		}
	}
}

func TestReloginMailboxRebuildsFromStoredCredentials(t *testing.T) {
	creds := map[string]any{
		"mail_provider": "cloudflare_temp_email",
		"mail_ref":      "cloudflare_temp_email#1",
		"mail_token":    "jwt-mail-token",
	}
	mailbox := reloginMailbox("user@example.test", creds, map[string]any{})
	for key, want := range map[string]string{
		"provider":     "cloudflare_temp_email",
		"address":      "user@example.test",
		"token":        "jwt-mail-token",
		"provider_ref": "cloudflare_temp_email#1",
	} {
		if got := util.Clean(mailbox[key]); got != want {
			t.Fatalf("mailbox[%s] = %q, want %q", key, got, want)
		}
	}
}
