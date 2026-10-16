package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FlareSolverr passes the Cloudflare challenge guarding an upstream host. A single
// request.get solve returns a cf_clearance cookie and the browser User-Agent that
// must be reused (cf_clearance is bound to the exact User-Agent and exit IP that
// solved it), so callers should adopt the returned UA and route through the same
// proxy that is passed here.

const flareSolverrMaxTimeoutMS = 120000

// SolveCloudflareChallenge calls a FlareSolverr endpoint to solve the challenge for
// target and returns the solved User-Agent and the cookies to inject. The proxy, if
// set, is forwarded to FlareSolverr so the solve happens from the same exit IP; see
// flareSolverrProxyPayload for how authenticated SOCKS upstreams are handled.
func SolveCloudflareChallenge(ctx context.Context, endpoint, target, proxy string) (string, []*http.Cookie, error) {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if base == "" {
		return "", nil, fmt.Errorf("flaresolverr endpoint is empty")
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	timeout := time.Duration(flareSolverrMaxTimeoutMS)*time.Millisecond + 30*time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	proxyPayload := flareSolverrProxyPayload(proxy)
	userAgent, cookies, err := solveCloudflareChallengeOnce(reqCtx, base, target, timeout, proxyPayload)
	if proxyPayload != nil && (err != nil || len(cookies) == 0) {
		// Chromium cannot authenticate against a SOCKS upstream: it either fails
		// outright (ERR_SOCKS_CONNECTION_FAILED) or bypasses the proxy and loads the
		// page through the direct exit, where FlareSolverr reports "Challenge not
		// detected!" with zero cookies. Retry once without the per-request proxy so
		// FlareSolverr falls back to its own configured upstream, which must resolve
		// to the same exit IP.
		if ua, cs, retryErr := solveCloudflareChallengeOnce(reqCtx, base, target, timeout, nil); retryErr == nil {
			return ua, cs, nil
		}
	}
	if err != nil {
		return "", nil, err
	}
	return userAgent, cookies, nil
}

// solveCloudflareChallengeOnce performs one FlareSolverr request.get call. A nil
// proxy payload lets FlareSolverr use its own configured upstream.
func solveCloudflareChallengeOnce(ctx context.Context, base, target string, timeout time.Duration, proxyPayload map[string]any) (string, []*http.Cookie, error) {
	reqBody := map[string]any{
		"cmd":        "request.get",
		"url":        target,
		"maxTimeout": flareSolverrMaxTimeoutMS,
	}
	if len(proxyPayload) > 0 {
		reqBody["proxy"] = proxyPayload
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(data))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			UserAgent string `json:"userAgent"`
			Status    int    `json:"status"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
				Path  string `json:"path"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(payload.Status, "ok") {
		return "", nil, fmt.Errorf("flaresolverr status=%q message=%q", payload.Status, payload.Message)
	}
	cookies := make([]*http.Cookie, 0, len(payload.Solution.Cookies))
	for _, item := range payload.Solution.Cookies {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		// Domain is intentionally left empty so the cookie jar binds it to the
		// request URL used at injection time. FlareSolverr may return a leading-dot
		// domain that some jars reject during SetCookies.
		cookies = append(cookies, &http.Cookie{
			Name:  item.Name,
			Value: item.Value,
			Path:  firstNonEmpty(item.Path, "/"),
		})
	}
	return payload.Solution.UserAgent, cookies, nil
}

// flareSolverrProxyPayload builds the per-request proxy object FlareSolverr hands to
// Chromium. Chromium can authenticate to an HTTP proxy but not to a SOCKS one
// (webRequest.onAuthRequired only covers HTTP auth), so an authenticated socks5 URL
// makes it either fail or silently bypass the proxy and solve from the direct exit.
// Mixed http/socks listeners are common, so such an endpoint is re-declared as
// http:// to keep the credentials working and preserve the single-exit-IP guarantee.
// SOCKS-only endpoints fall back to the upstream FlareSolverr itself is configured
// with via the retry in SolveCloudflareChallenge.
func flareSolverrProxyPayload(raw string) map[string]any {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return map[string]any{"url": value}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		return map[string]any{"url": value}
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	endpoint := scheme + "://" + parsed.Host
	if username == "" && password == "" {
		return map[string]any{"url": endpoint}
	}
	if strings.HasPrefix(scheme, "socks") {
		return map[string]any{"url": "http://" + parsed.Host, "username": username, "password": password}
	}
	return map[string]any{"url": endpoint, "username": username, "password": password}
}

// IsCloudflareChallengeText reports whether a response body looks like a Cloudflare
// challenge / "Just a moment" interstitial.
func IsCloudflareChallengeText(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "cf_chl") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "enable javascript and cookies to continue") ||
		strings.Contains(lower, "cloudflare")
}

// InjectCookies stores cookies in the client's cookie jar bound to target. It is a
// no-op when the client has no jar or there are no cookies. Returns the count
// injected.
func InjectCookies(client *http.Client, targetURL string, cookies []*http.Cookie) int {
	if client == nil || client.Jar == nil || len(cookies) == 0 {
		return 0
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return 0
	}
	client.Jar.SetCookies(parsed, cookies)
	return len(cookies)
}
