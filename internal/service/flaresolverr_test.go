package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlareSolverrProxyPayload(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    map[string]any
		wantNil bool
	}{
		{name: "empty disables the per-request proxy", raw: "", wantNil: true},
		{name: "plain http proxy", raw: "http://10.0.0.1:8080", want: map[string]any{"url": "http://10.0.0.1:8080"}},
		{name: "authenticated http proxy splits credentials", raw: "http://user:pass@10.0.0.1:8080", want: map[string]any{"url": "http://10.0.0.1:8080", "username": "user", "password": "pass"}},
		{name: "authenticated https proxy keeps scheme", raw: "https://user:pass@10.0.0.1:443", want: map[string]any{"url": "https://10.0.0.1:443", "username": "user", "password": "pass"}},
		{name: "authenticated socks5 is re-declared as http", raw: "socks5://socks:secret@192.0.2.10:1080", want: map[string]any{"url": "http://192.0.2.10:1080", "username": "socks", "password": "secret"}},
		{name: "authenticated socks5h is re-declared as http", raw: "socks5h://socks:secret@192.0.2.10:1080", want: map[string]any{"url": "http://192.0.2.10:1080", "username": "socks", "password": "secret"}},
		{name: "percent encoded credentials are decoded", raw: "socks5://us%40er:p%3Ass@192.0.2.10:1080", want: map[string]any{"url": "http://192.0.2.10:1080", "username": "us@er", "password": "p:ss"}},
		{name: "unauthenticated socks5 keeps scheme", raw: "socks5://192.0.2.10:1080", want: map[string]any{"url": "socks5://192.0.2.10:1080"}},
		{name: "value without a scheme is passed through", raw: "user:pass@192.0.2.10:1080", want: map[string]any{"url": "user:pass@192.0.2.10:1080"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flareSolverrProxyPayload(tc.raw)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("flareSolverrProxyPayload(%q) = %#v, want nil", tc.raw, got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("flareSolverrProxyPayload(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("flareSolverrProxyPayload(%q)[%q] = %#v, want %#v", tc.raw, key, got[key], want)
				}
			}
		})
	}
}

func writeFlareSolverrJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func readFlareSolverrBody(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body
}

func TestSolveCloudflareChallengeForwardsSplitProxyCredentials(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" {
			t.Errorf("path = %q, want /v1", r.URL.Path)
		}
		got = readFlareSolverrBody(r)
		writeFlareSolverrJSON(w, map[string]any{
			"status":  "ok",
			"message": "Challenge solved!",
			"solution": map[string]any{
				"userAgent": "ua-solved",
				"status":    200,
				"cookies": []map[string]any{
					{"name": "cf_clearance", "value": "clear", "path": "/"},
					{"name": "oai-did", "value": "device", "path": ""},
				},
			},
		})
	}))
	defer srv.Close()

	ua, cookies, err := SolveCloudflareChallenge(context.Background(), srv.URL, "https://auth.openai.com/", "socks5://socks:secret@192.0.2.10:1080")
	if err != nil {
		t.Fatalf("SolveCloudflareChallenge() error = %v", err)
	}
	if ua != "ua-solved" {
		t.Fatalf("userAgent = %q, want ua-solved", ua)
	}
	if len(cookies) != 2 {
		t.Fatalf("cookies = %#v, want 2", cookies)
	}
	if cookies[1].Path != "/" {
		t.Fatalf("blank cookie path should default to /, got %q", cookies[1].Path)
	}
	proxy, _ := got["proxy"].(map[string]any)
	if proxy == nil {
		t.Fatalf("request carried no proxy payload: %#v", got)
	}
	if proxy["url"] != "http://192.0.2.10:1080" || proxy["username"] != "socks" || proxy["password"] != "secret" {
		t.Fatalf("proxy payload = %#v", proxy)
	}
}

func TestSolveCloudflareChallengeRetriesWithoutProxyWhenNoCookies(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readFlareSolverrBody(r)
		calls = append(calls, body)
		if _, ok := body["proxy"]; ok {
			// Chromium bypassed the proxy and loaded the page from the direct exit.
			writeFlareSolverrJSON(w, map[string]any{
				"status":   "ok",
				"message":  "Challenge not detected!",
				"solution": map[string]any{"userAgent": "ua-direct", "status": 200, "cookies": []any{}},
			})
			return
		}
		writeFlareSolverrJSON(w, map[string]any{
			"status":  "ok",
			"message": "Challenge solved!",
			"solution": map[string]any{
				"userAgent": "ua-retry",
				"status":    200,
				"cookies":   []map[string]any{{"name": "cf_clearance", "value": "clear", "path": "/"}},
			},
		})
	}))
	defer srv.Close()

	ua, cookies, err := SolveCloudflareChallenge(context.Background(), srv.URL, "https://auth.openai.com/", "socks5://socks:secret@192.0.2.10:1080")
	if err != nil {
		t.Fatalf("SolveCloudflareChallenge() error = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("request count = %d, want 2", len(calls))
	}
	if _, ok := calls[0]["proxy"]; !ok {
		t.Fatalf("first request should carry the proxy payload: %#v", calls[0])
	}
	if _, ok := calls[1]["proxy"]; ok {
		t.Fatalf("retry should drop the proxy payload: %#v", calls[1])
	}
	if ua != "ua-retry" || len(cookies) != 1 || cookies[0].Name != "cf_clearance" {
		t.Fatalf("ua = %q, cookies = %#v", ua, cookies)
	}
}

func TestSolveCloudflareChallengeRetriesWithoutProxyOnFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body := readFlareSolverrBody(r)
		if _, ok := body["proxy"]; ok {
			writeFlareSolverrJSON(w, map[string]any{"status": "error", "message": "net::ERR_SOCKS_CONNECTION_FAILED"})
			return
		}
		writeFlareSolverrJSON(w, map[string]any{
			"status":  "ok",
			"message": "Challenge solved!",
			"solution": map[string]any{
				"userAgent": "ua-retry",
				"status":    200,
				"cookies":   []map[string]any{{"name": "cf_clearance", "value": "clear", "path": "/"}},
			},
		})
	}))
	defer srv.Close()

	ua, cookies, err := SolveCloudflareChallenge(context.Background(), srv.URL, "https://auth.openai.com/", "socks5://socks:secret@192.0.2.10:1080")
	if err != nil {
		t.Fatalf("SolveCloudflareChallenge() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if ua != "ua-retry" || len(cookies) != 1 {
		t.Fatalf("ua = %q, cookies = %#v", ua, cookies)
	}
}

func TestSolveCloudflareChallengeWithoutProxyDoesNotRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if _, ok := readFlareSolverrBody(r)["proxy"]; ok {
			t.Errorf("no proxy was configured but the request carried one")
		}
		writeFlareSolverrJSON(w, map[string]any{"status": "error", "message": "boom"})
	}))
	defer srv.Close()

	_, _, err := SolveCloudflareChallenge(context.Background(), srv.URL, "https://auth.openai.com/", "")
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want it to mention boom", err)
	}
	if calls != 1 {
		t.Fatalf("request count = %d, want 1", calls)
	}
}
