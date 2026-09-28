package service

// ChatGPT registration flow ported from Sakuralaaa/gpt-auto-register
// (auth_flow.py run_register). This is the pure-protocol path that currently
// works end to end:
//
//	warmup chatgpt.com (oai-did cookie) -> csrf -> signin/openai ->
//	authorize init -> sentinel -> authorize/continue(signup) ->
//	register password -> OTP send/wait/validate -> create_account ->
//	redirect chain -> consume callback -> /api/auth/session (access_token)
//
// Key fidelity notes carried over from the Python implementation:
//   - warmup must plant the oai-did cookie or authorize/continue will 409.
//   - navigation requests need the full Sec-Fetch-* + client hints header set.
//   - after POST user/register the server switches to a new challenge, so the
//     OTP must be (re)sent after password registration and timestamped from
//     before that request.
//   - the callback URL must be consumed by chatgpt.com NextAuth before
//     /api/auth/session returns a fresh access_token.
//   - Codex token exchange is intentionally skipped (user requirement).

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)


const (
	registerChatGPTBase   = "https://chatgpt.com"
	registerSentinelFlow0 = "authorize_continue"
)

var registerContinuePathHints = []string{"/create-account/password", "/email-verification", "/about-you"}

type registerFlowResult struct {
	Email        string
	Password     string
	AccessToken  string
	SessionToken string
}

type registerFlow struct {
	w        *registerWorker
	ctx      context.Context
	email    string
	password string
	deviceID string
	// sentinel tokens from the most recent successful mint
	sentinelToken   string
	sentinelSOToken string
	// state discovered along the flow
	isExistingAccount             bool
	existingVerificationMode      string
	existingPageType              string
	csrfToken                     string
	authURL                       string
	screenHint                    string
	authSessionFetched            bool
	existingAccountVerificationM1 string
}

func newRegisterFlow(w *registerWorker, ctx context.Context) *registerFlow {
	return &registerFlow{w: w, ctx: ctx, deviceID: w.deviceID}
}

func (f *registerFlow) ua() string { return f.w.flowUA() }

// step logs into the shared register log stream.
func (f *registerFlow) step(format string, args ...any) {
	f.w.step(fmt.Sprintf(format, args...))
}

// ── Step 0: warmup — plant chatgpt.com cookies incl. oai-did ────────────────

func (f *registerFlow) warmup() bool {
	headers := f.navigationHeaders("")
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(3+attempt*2) * time.Second)
		}
		req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, registerChatGPTBase+"/", nil)
		if err != nil {
			return false
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := f.w.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if f.hasCookie(registerChatGPTBase, "oai-did") {
				f.step("warmup 完成（第 %d 次，oai-did 已种）", attempt+1)
				return true
			}
			f.step("warmup 第 %d/4 次未种到 oai-did（HTTP %d）", attempt+1, resp.StatusCode)
		} else {
			f.step("warmup 第 %d/4 次请求失败: %v", attempt+1, err)
		}
	}
	f.step("warmup 4 次均未种到 oai-did，继续注册必然 409 invalid_state")
	return false
}

func (f *registerFlow) hasCookie(rawURL, name string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || f.w.client.Jar == nil {
		return false
	}
	for _, cookie := range f.w.client.Jar.Cookies(parsed) {
		if cookie.Name == name && strings.TrimSpace(cookie.Value) != "" {
			return true
		}
	}
	return false
}

func (f *registerFlow) cookieValue(rawURL, name string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || f.w.client.Jar == nil {
		return ""
	}
	for _, cookie := range f.w.client.Jar.Cookies(parsed) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// ── Header builders (ported from _common_headers / _navigation_headers) ─────

func (f *registerFlow) originOf(referer string) string {
	parsed, err := url.Parse(referer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "https://chatgpt.com"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func (f *registerFlow) commonHeaders(referer string) map[string]string {
	origin := f.originOf(referer)
	fp := f.w.fp
	headers := map[string]string{
		"Accept":          "application/json",
		"Referer":         referer,
		"Origin":          origin,
		"User-Agent":      f.ua(),
		"Accept-Language": "en-US,en;q=0.9",
		"Sec-Fetch-Dest":  "empty",
		"Sec-Fetch-Mode":  "cors",
		"Sec-Fetch-Site":  "same-origin",
		"priority":        "u=1, i",
	}
	if fp.secChUA != "" {
		headers["sec-ch-ua"] = fp.secChUA
		headers["sec-ch-ua-mobile"] = "?0"
		headers["sec-ch-ua-platform"] = `"Windows"`
		headers["sec-ch-ua-full-version-list"] = fp.secChUAFullVersionList
		headers["sec-ch-ua-arch"] = `"x86"`
		headers["sec-ch-ua-bitness"] = `"64"`
		headers["sec-ch-ua-model"] = `""`
		headers["sec-ch-ua-platform-version"] = fp.secChUAPlatformVersion
	}
	if strings.Contains(origin, "auth.openai.com") {
		if deviceID := strings.TrimSpace(f.deviceID); deviceID != "" {
			headers["oai-device-id"] = deviceID
		}
	}
	for key, value := range registerTraceHeaders() {
		headers[key] = value
	}
	return headers
}

func (f *registerFlow) navigationHeaders(referer string) map[string]string {
	headers := map[string]string{
		"accept":                     "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language":            "en-US,en;q=0.9",
		"sec-fetch-dest":             "document",
		"sec-fetch-mode":             "navigate",
		"sec-fetch-site":             "none",
		"sec-fetch-user":             "?1",
		"upgrade-insecure-requests":  "1",
		"priority":                   "u=0, i",
		"User-Agent":                 f.ua(),
		"sec-ch-ua":                  f.w.fp.secChUA,
		"sec-ch-ua-mobile":           "?0",
		"sec-ch-ua-platform":         `"Windows"`,
		"sec-ch-ua-full-version-list": f.w.fp.secChUAFullVersionList,
		"sec-ch-ua-arch":             `"x86"`,
		"sec-ch-ua-bitness":          `"64"`,
		"sec-ch-ua-model":            `""`,
		"sec-ch-ua-platform-version": f.w.fp.secChUAPlatformVersion,
	}
	if referer != "" {
		headers["Referer"] = referer
	}
	return headers
}

// ── Step 1: CSRF token ───────────────────────────────────────────────────────

func (f *registerFlow) getCSRFToken() (string, error) {
	headers := f.commonHeaders(registerChatGPTBase + "/auth/login")
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*5) * time.Second)
		}
		status, payload, err := f.w.request(f.ctx, http.MethodGet, registerChatGPTBase+"/api/auth/csrf", nil, headers, false)
		if err != nil {
			lastErr = err
			continue
		}
		if status == http.StatusForbidden && attempt < 2 {
			f.step("Cloudflare 403（csrf），重试 %d/3", attempt+1)
			lastErr = fmt.Errorf("csrf_http_403")
			continue
		}
		if status != http.StatusOK {
			lastErr = fmt.Errorf("csrf_http_%d", status)
			continue
		}
		csrf := util.Clean(payload["csrfToken"])
		if csrf == "" {
			return "", fmt.Errorf("CSRF Token 获取失败")
		}
		f.csrfToken = csrf
		return csrf, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("csrf request failed")
	}
	return "", lastErr
}

// ── Step 2: signin/openai -> authorize URL ──────────────────────────────────

func (f *registerFlow) getAuthURL(csrfToken string) (string, error) {
	headers := f.commonHeaders(registerChatGPTBase + "/auth/login")
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	params := url.Values{
		"prompt":                          {"login"},
		"screen_hint":                     {"login_or_signup"},
		"ext-oai-did":                     {f.deviceID},
		"auth_session_logging_id":         {util.NewUUID()},
		"ext-passkey-client-capabilities": {"1111"},
		"login_hint":                      {f.email},
	}
	signinURL := registerChatGPTBase + "/api/auth/signin/openai?" + params.Encode()
	form := url.Values{
		"csrfToken":   {csrfToken},
		"callbackUrl": {registerChatGPTBase + "/"},
		"json":        {"true"},
	}
	status, payload, err := f.w.requestFormWithHeaders(f.ctx, signinURL, form, headers)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("signin_openai_http_%d", status)
	}
	authURL := util.Clean(payload["url"])
	if authURL == "" {
		return "", fmt.Errorf("Auth URL 获取失败")
	}
	f.authURL = authURL
	return authURL, nil
}

// ── Step 3: follow authorize chain, resolve device id ───────────────────────

func (f *registerFlow) authOAuthInit(authURL string) (string, error) {
	headers := f.navigationHeaders(registerChatGPTBase + "/")
	headers["Referer"] = registerChatGPTBase + "/"
	headers["sec-fetch-site"] = "cross-site"
	delete(headers, "sec-fetch-user")
	status, _, err := f.w.request(f.ctx, http.MethodGet, authURL, nil, headers, true)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK && status != http.StatusFound {
		f.step("auth_oauth_init HTTP %d", status)
	}
	deviceID := f.cookieValue(registerAuthBase, "oai-did")
	if deviceID == "" {
		deviceID = f.cookieValue(registerChatGPTBase, "oai-did")
	}
	if deviceID == "" {
		deviceID = strings.TrimSpace(f.deviceID)
	}
	f.deviceID = deviceID
	return deviceID, nil
}

// ── Step 4: sentinel (authorize_continue flow) ──────────────────────────────

func (f *registerFlow) refreshSentinel(flow string) error {
	artifacts, err := f.w.buildSentinelArtifactsWithFallback(f.ctx, flow)
	if err != nil {
		return err
	}
	f.sentinelToken = artifacts.token
	f.sentinelSOToken = artifacts.soToken
	return nil
}

func (f *registerFlow) applySentinelHeaders(headers map[string]string) {
	if f.sentinelToken != "" {
		headers["openai-sentinel-token"] = f.sentinelToken
	}
	if f.sentinelSOToken != "" {
		headers["openai-sentinel-so-token"] = f.sentinelSOToken
	}
}

// ── Step 5: authorize/continue (signup) ─────────────────────────────────────

func (f *registerFlow) authorizeContinue(email, screenHint, referer string) (map[string]any, error) {
	if err := f.refreshSentinel(registerSentinelFlow0); err != nil {
		return nil, fmt.Errorf("sentinel(%s): %w", screenHint, err)
	}
	headers := f.commonHeaders(referer)
	headers["Content-Type"] = "application/json"
	f.applySentinelHeaders(headers)
	payload := map[string]any{
		"username":    map[string]any{"value": email, "kind": "email"},
		"screen_hint": screenHint,
	}
	status, resp, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/authorize/continue", payload, headers, false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("authorize/continue 失败(screen_hint=%s): HTTP %d%s", screenHint, status, registerResponseDetail(resp))
	}
	return resp, nil
}

// signup submits the email and reports whether this is a new account.
func (f *registerFlow) signup(email string) (bool, error) {
	data, err := f.authorizeContinue(email, "signup", registerAuthBase+"/create-account")
	if err != nil {
		return false, err
	}
	page := util.StringMap(data["page"])
	pageType := util.Clean(page["type"])
	payloadPage := util.StringMap(page["payload"])
	continueURL := util.Clean(data["continue_url"])

	switch {
	case pageType == "create_account_password" || strings.Contains(continueURL, "/create-account/password"):
		f.isExistingAccount = false
		f.existingVerificationMode = ""
		f.existingPageType = pageType
		return true, nil
	case pageType == "email_otp_verification":
		mode := util.Clean(payloadPage["email_verification_mode"])
		f.existingVerificationMode = mode
		f.existingPageType = pageType
		if mode == "passwordless_signup" {
			f.isExistingAccount = false
		} else {
			f.isExistingAccount = true
		}
		return false, nil
	default:
		f.existingVerificationMode = util.Clean(payloadPage["email_verification_mode"])
		f.existingPageType = pageType
		f.isExistingAccount = true
		f.step("authorize/continue 返回非标准页面 page_type=%s，按已有账号处理", pageType)
		return false, nil
	}
}

// ── Step 6: register password (new accounts only) ───────────────────────────

func (f *registerFlow) registerPassword(email string) bool {
	password := registerRandomPassword(16)
	f.password = password

	// Establish server-side state via the password page.
	pageHeaders := f.navigationHeaders(registerAuthBase + "/create-account")
	if _, _, err := f.w.request(f.ctx, http.MethodGet, registerAuthBase+"/create-account/password", nil, pageHeaders, false); err != nil {
		f.step("访问 create-account/password 页面失败: %v", err)
	}

	if err := f.refreshSentinel("username_password_create"); err != nil {
		f.step("注册前刷新 sentinel 失败，沿用现有 token: %v", err)
	}
	// username_password_create never has an SO block per server behaviour; only
	// attach the SO header when one was minted for this flow.
	soToken := f.sentinelSOToken
	headers := f.commonHeaders(registerAuthBase + "/create-account/password")
	headers["Content-Type"] = "application/json"
	f.applySentinelHeaders(headers)
	if soToken == "" {
		delete(headers, "openai-sentinel-so-token")
	}
	status, _, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/user/register", map[string]any{
		"password": password,
		"username": email,
	}, headers, false)
	if err != nil {
		f.step("密码注册请求异常: %v", err)
		return false
	}
	if status != http.StatusOK {
		f.step("密码注册返回 %d", status)
		return false
	}
	f.step("密码注册成功")
	return true
}

// ── Step 7: OTP send helpers ────────────────────────────────────────────────

func (f *registerFlow) sendOTP(referer string) error {
	headers := f.commonHeaders(referer)
	f.applySentinelHeaders(headers)
	status, _, err := f.w.request(f.ctx, http.MethodGet, registerAuthBase+"/api/accounts/email-otp/send", nil, headers, false)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("发送 OTP 失败: %d", status)
	}
	return nil
}

func (f *registerFlow) sendPasswordlessOTP(referer string) bool {
	headers := f.commonHeaders(referer)
	headers["Content-Type"] = "application/json"
	f.applySentinelHeaders(headers)
	status, _, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/passwordless/send-otp", nil, headers, false)
	return err == nil && status == http.StatusOK
}

func (f *registerFlow) resendOTP(referer string) bool {
	headers := f.commonHeaders(referer)
	headers["Content-Type"] = "application/json"
	f.applySentinelHeaders(headers)
	status, _, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/email-otp/resend", nil, headers, false)
	return err == nil && status == http.StatusOK
}

// kickoffOTPDelivery mirrors the unified send strategy: existing-account
// states may only resend (send creates a new challenge and voids the mail in
// flight), new registrations prefer passwordless send then resend then send.
func (f *registerFlow) kickoffOTPDelivery(mode string) bool {
	modeLC := strings.ToLower(strings.TrimSpace(mode))
	isExisting := strings.Contains(modeLC, "existing") ||
		strings.Contains(modeLC, "passwordless_login") ||
		strings.Contains(modeLC, "passwordless_signup") ||
		f.isExistingAccount
	if isExisting {
		if f.resendOTP(registerAuthBase + "/email-verification") {
			return true
		}
		if err := f.sendOTP(registerAuthBase + "/email-verification"); err == nil {
			return true
		}
		return false
	}
	if f.sendPasswordlessOTP(registerAuthBase + "/create-account/password") {
		return true
	}
	if f.resendOTP(registerAuthBase + "/email-verification") {
		return true
	}
	if err := f.sendOTP(registerAuthBase + "/create-account/password"); err == nil {
		return true
	}
	return false
}

// ── Step 8: validate OTP ────────────────────────────────────────────────────

func (f *registerFlow) validateOTP(code string) (map[string]any, error) {
	headers := f.commonHeaders(registerAuthBase + "/email-verification")
	headers["Content-Type"] = "application/json"
	status, resp, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/email-otp/validate", map[string]any{"code": code}, headers, false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("OTP 验证失败: %d - %s", status, registerResponseDetail(resp))
	}
	return resp, nil
}

// ── Step 9: create account (about-you) ──────────────────────────────────────

func (f *registerFlow) createAccount() (string, error) {
	if err := f.refreshSentinel("oauth_create_account"); err != nil {
		f.step("创建账户前刷新 sentinel 失败: %v", err)
	}
	headers := f.commonHeaders(registerAuthBase + "/about-you")
	headers["Content-Type"] = "application/json"
	f.applySentinelHeaders(headers)
	first, last := registerRandomName()
	name := first + " " + last
	birthdate := fmt.Sprintf("%04d-%02d-%02d", 1985+rand.Intn(16), 1+rand.Intn(12), 1+rand.Intn(28))
	status, resp, err := f.w.request(f.ctx, http.MethodPost, registerAuthBase+"/api/accounts/create_account", map[string]any{
		"name":      name,
		"birthdate": birthdate,
	}, headers, false)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("创建账户失败: %d - %s", status, registerResponseDetail(resp))
	}
	continueURL := util.Clean(resp["continue_url"])
	if continueURL == "" {
		return "", fmt.Errorf("创建账户后未获取到 continue_url")
	}
	return continueURL, nil
}

// ── Step 10: redirect chain → callback → session ────────────────────────────

func (f *registerFlow) followRedirectChain(startURL string) (string, string) {
	current := startURL
	callback := ""
	referer := registerAuthBase + "/"
	for hop := 0; hop < 12; hop++ {
		headers := f.navigationHeaders(referer)
		headers["Referer"] = referer
		delete(headers, "sec-fetch-user")
		currentParsed, err := url.Parse(current)
		refererParsed, _ := url.Parse(referer)
		if err == nil && refererParsed != nil {
			if currentParsed.Host == refererParsed.Host {
				headers["sec-fetch-site"] = "same-origin"
			} else {
				headers["sec-fetch-site"] = "cross-site"
			}
		}
		status, _, header, reqErr := f.w.requestDetailed(f.ctx, http.MethodGet, current, nil, headers, false)
		if reqErr != nil {
			break
		}
		referer = current
		if strings.Contains(current, "/api/auth/callback/openai") {
			callback = current
		}
		if status == http.StatusOK && strings.Contains(current, "/workspace") {
			// workspace pages return 200; nothing to follow server-side here
			break
		}
		if isRedirectStatus(status) {
			location := strings.TrimSpace(header.Get("Location"))
			if location == "" {
				break
			}
			next, err := resolveRegisterLocation(current, location)
			if err != nil {
				break
			}
			if strings.Contains(next, "/api/auth/callback/openai") && strings.Contains(next, "code=") {
				callback = next
				break
			}
			current = next
			continue
		}
		break
	}
	return callback, current
}

func isRedirectStatus(status int) bool {
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect ||
		status == http.StatusPermanentRedirect
}

// consumeCallback GETs the callback URL so chatgpt.com NextAuth plants the
// __Secure-next-auth.session-token cookie, then returns whether it landed.
func (f *registerFlow) consumeCallback(callbackURL string) bool {
	if callbackURL == "" || !strings.Contains(callbackURL, "code=") {
		return false
	}
	current := callbackURL
	headers := map[string]string{
		"Accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Referer":    registerAuthBase + "/",
		"User-Agent": f.ua(),
	}
	for hop := 0; hop < 8; hop++ {
		status, _, header, err := f.w.requestDetailed(f.ctx, http.MethodGet, current, nil, headers, false)
		if err != nil {
			return false
		}
		if !isRedirectStatus(status) {
			break
		}
		location := strings.TrimSpace(header.Get("Location"))
		if location == "" {
			break
		}
		next, err := resolveRegisterLocation(current, location)
		if err != nil {
			break
		}
		current = next
		parsed, parseErr := url.Parse(current)
		if parseErr == nil && strings.Contains(parsed.Host, "chatgpt.com") && !strings.Contains(current, "/api/auth/callback") {
			// land on the homepage so all cookies settle
			homeHeaders := f.navigationHeaders(registerAuthBase + "/")
			_, _, _, _ = f.w.requestDetailed(f.ctx, http.MethodGet, current, nil, homeHeaders, true)
			break
		}
	}
	return f.hasCookie(registerChatGPTBase, "__Secure-next-auth.session-token")
}

func (f *registerFlow) getAuthSession() (string, string) {
	headers := f.commonHeaders(registerChatGPTBase + "/")
	status, payload, err := f.w.request(f.ctx, http.MethodGet, registerChatGPTBase+"/api/auth/session", nil, headers, false)
	if err != nil || status != http.StatusOK {
		if err == nil {
			f.step("auth/session HTTP %d", status)
		}
	} else {
		if token := util.Clean(payload["accessToken"]); token != "" {
			return f.cookieValue(registerChatGPTBase, "__Secure-next-auth.session-token"), token
		}
	}
	sessionToken := f.cookieValue(registerChatGPTBase, "__Secure-next-auth.session-token")
	accessToken := util.Clean(payload["accessToken"])
	return sessionToken, accessToken
}

// reauthorizeForSession re-runs authorize (without prompt=login) to obtain a
// callback URL when the OTP response lacks a continue_url.
func (f *registerFlow) reauthorizeForSession() string {
	if f.authURL == "" {
		return ""
	}
	parsed, err := url.Parse(f.authURL)
	if err != nil {
		return ""
	}
	params := parsed.Query()
	params.Del("prompt")
	parsed.RawQuery = params.Encode()
	authorizeURL := parsed.String()
	headers := f.navigationHeaders(registerChatGPTBase + "/")
	status, _, header, reqErr := f.w.requestDetailed(f.ctx, http.MethodGet, authorizeURL, nil, headers, false)
	if reqErr != nil {
		return ""
	}
	current := ""
	if isRedirectStatus(status) {
		current = strings.TrimSpace(header.Get("Location"))
	} else if status == http.StatusOK {
		current = authorizeURL
	}
	for hop := 0; hop < 10 && current != ""; hop++ {
		if strings.Contains(current, "code=") && strings.Contains(current, "state=") {
			return current
		}
		hopHeaders := f.navigationHeaders(refererOf(authorizeURL))
		hopStatus, _, hopHeader, hopErr := f.w.requestDetailed(f.ctx, http.MethodGet, current, nil, hopHeaders, false)
		if hopErr != nil {
			break
		}
		if !isRedirectStatus(hopStatus) {
			break
		}
		next := strings.TrimSpace(hopHeader.Get("Location"))
		if next == "" {
			break
		}
		resolved, resolveErr := resolveRegisterLocation(current, next)
		if resolveErr != nil {
			break
		}
		current = resolved
	}
	return ""
}

func refererOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return registerAuthBase
	}
	return parsed.Scheme + "://" + parsed.Host
}

// ── Orchestration: run_register ─────────────────────────────────────────────

func (f *registerFlow) run(mailbox map[string]any) (registerFlowResult, error) {
	result := registerFlowResult{Email: f.email}

	if !f.warmup() {
		return result, fmt.Errorf("warmup 失败：4 次重试均未拿到 chatgpt.com 的 oai-did cookie，继续注册必然 409 invalid_state（多为出口 IP 被 CF 拦），请配置可用代理后重试")
	}

	csrf, err := f.getCSRFToken()
	if err != nil {
		return result, err
	}
	authURL, err := f.getAuthURL(csrf)
	if err != nil {
		return result, err
	}
	if _, err := f.authOAuthInit(authURL); err != nil {
		return result, err
	}
	isNew, err := f.signup(f.email)
	if err != nil {
		return result, err
	}

	var continueURL string
	otpTimeout := registerOTPTimeout(f.w)

	if isNew || f.existingVerificationMode == "passwordless_signup" {
		passwordRegistered := f.registerPassword(f.email)
		// After POST user/register the server switches challenge; the OTP must
		// be re-sent now and timestamped before that request.
		otpSentAt := time.Now()
		if passwordRegistered {
			if err := f.sendOTP(registerAuthBase + "/create-account/password"); err != nil {
				f.step("密码注册后主动发码失败，回退 resend: %v", err)
				f.kickoffOTPDelivery("post_register_password_send_failed")
			}
		} else {
			f.kickoffOTPDelivery("register_password_failed_fallback")
			otpSentAt = time.Now()
		}
		code, waitErr := f.waitForOTP(mailbox, otpTimeout, otpSentAt)
		if waitErr != nil {
			return result, waitErr
		}
		if _, err := f.validateOTP(code); err != nil {
			if strings.Contains(err.Error(), "401") {
				f.step("OTP 首次验证失败，补发重试: %v", err)
				otpSentAt = time.Now()
				f.kickoffOTPDelivery("verify_otp_retry_new")
				code, waitErr = f.waitForOTP(mailbox, otpTimeout, otpSentAt)
				if waitErr != nil {
					return result, waitErr
				}
				if _, err := f.validateOTP(code); err != nil {
					return result, err
				}
			} else {
				return result, err
			}
		}
		continueURL, err = f.createAccount()
		if err != nil {
			return result, err
		}
	} else {
		// Existing account: authorize/continue already triggered a send when the
		// mode is passwordless; otherwise send explicitly, then wait.
		mode := strings.ToLower(f.existingVerificationMode)
		otpSentAt := time.Now().Add(-8 * time.Second)
		if mode != "passwordless_signup" && mode != "passwordless_login" {
			if err := f.sendOTP(registerAuthBase + "/email-verification"); err != nil {
				f.step("已有账号发码失败: %v", err)
			} else {
				otpSentAt = time.Now()
			}
		} else {
			f.kickoffOTPDelivery("existing_passwordless")
			otpSentAt = time.Now()
		}
		code, waitErr := f.waitForOTP(mailbox, otpTimeout, otpSentAt)
		if waitErr != nil {
			return result, waitErr
		}
		otpResp, err := f.validateOTP(code)
		if err != nil {
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "409") {
				f.step("已有账号 OTP 首次验证失败，重发重试: %v", err)
				otpSentAt = time.Now()
				f.kickoffOTPDelivery("existing_verify_retry")
				code, waitErr = f.waitForOTP(mailbox, otpTimeout, otpSentAt)
				if waitErr != nil {
					return result, waitErr
				}
				otpResp, err = f.validateOTP(code)
				if err != nil {
					return result, err
				}
			} else {
				return result, err
			}
		}
		continueURL = util.Clean(otpResp["continue_url"])
		if continueURL == "" {
			continueURL = f.reauthorizeForSession()
		}
	}

	callbackURL := ""
	if continueURL != "" {
		callbackURL, _ = f.followRedirectChain(continueURL)
	}
	if callbackURL != "" {
		f.consumeCallback(callbackURL)
	}
	sessionToken, accessToken := f.getAuthSession()
	if accessToken == "" && callbackURL == "" && continueURL != "" {
		// one more try after a reauthorize
		if retry := f.reauthorizeForSession(); retry != "" {
			if cb, _ := f.followRedirectChain(retry); cb != "" {
				f.consumeCallback(cb)
			}
			_, accessToken = f.getAuthSession()
		}
	}
	result.SessionToken = sessionToken
	result.AccessToken = accessToken
	if accessToken == "" {
		return result, fmt.Errorf("注册完成但未获取有效 access_token")
	}
	f.step("注册流程完成")
	return result, nil
}

func registerOTPTimeout(w *registerWorker) time.Duration {
	mailCfg := util.StringMap(w.config["mail"])
	seconds := util.ToInt(mailCfg["otp_timeout"], 0)
	if seconds <= 0 {
		seconds = util.ToInt(w.config["otp_timeout"], 60)
	}
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func (f *registerFlow) waitForOTP(mailbox map[string]any, timeout time.Duration, issuedAfter time.Time) (string, error) {
	// waitRegisterCode polls the configured provider on its own interval; we
	// wrap it with the OTP-level deadline so a stuck provider still yields to
	// the flow's overall OTP timeout.
	deadline := time.Now().Add(timeout)
	for {
		code, err := waitRegisterCode(f.ctx, util.StringMap(f.w.config["mail"]), mailbox)
		if err == nil && code != "" {
			return code, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("等待注册验证码超时（%ds）", int(timeout.Seconds()))
		}
		time.Sleep(2 * time.Second)
	}
}
