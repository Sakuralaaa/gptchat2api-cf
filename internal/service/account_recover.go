package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/util"
)

// AccountRecovery recovers legacy pool accounts that only carry an access
// token: it replays a passwordless email-OTP login with the stored email,
// then ingests the renewed tokens together with every self-healing
// credential (session_token, email; mail credentials when the CF admin API
// can mint a fresh mailbox jwt for the same address).
type AccountRecovery struct {
	mu       sync.Mutex
	accounts *AccountService
	register *RegisterService
	// pending keeps one in-flight login session per email between the
	// "start" and "confirm" calls of the recovery dialog.
	pending map[string]*recoverySession
}

type recoverySession struct {
	worker     *registerWorker
	flow       *registerFlow
	email      string
	createdAt  time.Time
	autoOTP    bool
	mailConfig map[string]any
	mailbox    map[string]any
}

// NewAccountRecovery wires the recovery helper against the pool and the
// register service (for mail configuration and worker construction).
func NewAccountRecovery(accounts *AccountService, register *RegisterService) *AccountRecovery {
	return &AccountRecovery{
		accounts: accounts,
		register: register,
		pending:  map[string]*recoverySession{},
	}
}

// StartRecovery kicks off the passwordless login for the given emails. For
// every email it returns whether a verification code will be sent; emails
// that fail to start are reported with their error. The caller completes the
// flow with ConfirmRecovery(email, code).
func (r *AccountRecovery) StartRecovery(emails []string) (started map[string]bool, errors map[string]string) {
	started = map[string]bool{}
	errors = map[string]string{}
	for i, email := range emails {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") {
			errors[email] = "无效邮箱地址"
			continue
		}
		if err := r.startOne(email); err != nil {
			errors[email] = err.Error()
			continue
		}
		started[email] = true
	}
	return started, errors
}

func (r *AccountRecovery) startOne(email string) error {
	config := cloneMap(r.register.Get())
	config["mail"] = cloneMap(util.StringMap(config["mail"]))
	worker, err := newRegisterWorker(r.register, 0, config)
	if err != nil {
		return fmt.Errorf("创建登录会话失败: %w", err)
	}
	ctx := context.Background()
	flow := newRegisterFlow(worker, ctx)
	flow.email = email
	if err := worker.prewarmCloudflare(ctx); err != nil {
		worker.step("找回登录 Cloudflare 预热失败（继续尝试）: " + err.Error())
	}
	if !flow.warmup() {
		worker.close()
		return fmt.Errorf("warmup 失败：chatgpt.com 未种到 oai-did cookie")
	}
	csrf, err := flow.getCSRFToken()
	if err != nil {
		worker.close()
		return fmt.Errorf("获取 CSRF 失败: %w", err)
	}
	authURL, err := flow.getAuthURL(csrf)
	if err != nil {
		worker.close()
		return fmt.Errorf("获取 Auth URL 失败: %w", err)
	}
	if _, err := flow.authOAuthInit(authURL); err != nil {
		worker.close()
		return fmt.Errorf("authorize 初始化失败: %w", err)
	}
	// Mirror the register link: submit the email through the flow's
	// authorize/continue (sentinel token), retrying transient upstream
	// 409/429 responses exactly like the register flow does.
	var isNew bool
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(15*attempt) * time.Second
			worker.step(fmt.Sprintf("上游限流/冲突，%s 后重试(%d/3)", delay, attempt+1))
			time.Sleep(delay)
		}
		isNew, lastErr = flow.signup(email)
		if lastErr == nil {
			break
		}
		if !strings.Contains(lastErr.Error(), "HTTP 429") && !strings.Contains(lastErr.Error(), "HTTP 409") {
			break
		}
	}
	if lastErr != nil {
		worker.close()
		return fmt.Errorf("提交邮箱失败: %w", lastErr)
	}
	if isNew {
		worker.close()
		return fmt.Errorf("该邮箱未注册过 ChatGPT 账号（上游返回了注册页）")
	}
	// The login page answers with the password challenge for accounts that
	// carry a password (legacy registrations always did). Switch to the
	// one-time email code login — the "使用邮箱验证码登录" option — via the
	// passwordless send-otp endpoint, then wait for the code as usual.
	pageType := flow.existingPageType
	mode := strings.ToLower(flow.existingVerificationMode)
	if pageType == "login_password" || strings.Contains(pageType, "password") {
		worker.step("上游要求密码登录，切换为邮箱验证码登录")
		if !flow.sendPasswordlessOTP(registerAuthBase + "/log-in/password") {
			worker.close()
			return fmt.Errorf("切换邮箱验证码登录失败（passwordless/send-otp）")
		}
	} else if mode != "passwordless_login" && mode != "passwordless_signup" {
		// Existing-account OTP challenge without an auto-dispatched code.
		if err := flow.sendOTP(registerAuthBase + "/email-verification"); err != nil {
			if !flow.resendOTP(registerAuthBase + "/email-verification") {
				worker.close()
				return fmt.Errorf("发送验证码失败: %w", err)
			}
		}
	}

	r.mu.Lock()
	if old := r.pending[email]; old != nil {
		old.worker.close()
	}
	r.pending[email] = &recoverySession{
		worker:     worker,
		flow:       flow,
		email:      email,
		createdAt:  time.Now(),
		autoOTP:    recoveryAutoOTPAvailable(util.StringMap(config["mail"]), email),
		mailConfig: util.StringMap(config["mail"]),
	}
	r.mu.Unlock()
	worker.step(fmt.Sprintf("找回登录已发起，验证码已发送到 %s", email))
	return nil
}

// recoveryAutoOTPAvailable reports whether the configured mail providers can
// fetch mail for this address without user interaction: only the CF temp-mail
// admin API can read arbitrary inboxes of its own domains.
func recoveryAutoOTPAvailable(mailConfig map[string]any, email string) bool {
	local, domain, ok := strings.Cut(strings.ToLower(email), "@")
	if !ok || local == "" || domain == "" {
		return false
	}
	for _, entry := range util.AsMapSlice(mailConfig["providers"]) {
		if !util.ToBool(entry["enable"]) || util.Clean(entry["type"]) != "cloudflare_temp_email" {
			continue
		}
		if util.Clean(entry["admin_password"]) == "" {
			continue
		}
		for _, d := range util.AsStringSlice(entry["domain"]) {
			if strings.EqualFold(strings.TrimSpace(d), domain) {
				return true
			}
		}
	}
	return false
}

// FetchRecoveryCode tries to pull the newest OTP from the CF temp-mail admin
// API for the given address. It returns "" when the mailbox has no new mail.
func (r *AccountRecovery) FetchRecoveryCode(email string) (string, error) {
	r.mu.Lock()
	session := r.pending[strings.ToLower(strings.TrimSpace(email))]
	r.mu.Unlock()
	if session == nil {
		return "", fmt.Errorf("该邮箱没有进行中的找回会话")
	}
	local, _, _ := strings.Cut(session.email, "@")
	mailbox := map[string]any{"address": session.email, "token": ""}
	_ = local
	code := ""
	for attempt := 0; attempt < 20; attempt++ {
		provider, err := createRegisterMailProvider(session.mailConfig, "cloudflare_temp_email", "")
		if err != nil {
			return "", err
		}
		message, fetchErr := provider.FetchLatestMessage(mailbox)
		provider.Close()
		if fetchErr == nil && message != nil {
			if got := extractRegisterMailCode(message); got != "" {
				code = got
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	return code, nil
}

// ConfirmRecovery validates the code (user-pasted or auto-fetched) and
// ingests the recovered account with its renewed tokens.
func (r *AccountRecovery) ConfirmRecovery(email, code string) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	r.mu.Lock()
	session := r.pending[email]
	r.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("该邮箱没有进行中的找回会话，请重新发起")
	}
	if code == "" {
		if session.autoOTP {
			fetched, err := r.FetchRecoveryCode(email)
			if err != nil || fetched == "" {
				return nil, fmt.Errorf("自动收码失败，请手动粘贴验证码")
			}
			code = fetched
		} else {
			return nil, fmt.Errorf("请输入验证码")
		}
	}
	otpPayload, err := session.flow.validateOTP(code)
	if err != nil {
		return nil, fmt.Errorf("验证码校验失败: %w", err)
	}
	continueURL := util.Clean(otpPayload["continue_url"])
	if continueURL == "" {
		continueURL = session.flow.reauthorizeForSession()
	}
	callbackURL := ""
	if continueURL != "" {
		callbackURL, _ = session.flow.followRedirectChain(continueURL)
	}
	if callbackURL != "" {
		session.flow.consumeCallback(callbackURL)
	}
	sessionToken, accessToken := session.flow.getAuthSession()
	if accessToken == "" && callbackURL == "" && continueURL != "" {
		if retry := session.flow.reauthorizeForSession(); retry != "" {
			if cb, _ := session.flow.followRedirectChain(retry); cb != "" {
				session.flow.consumeCallback(cb)
			}
			_, accessToken = session.flow.getAuthSession()
		}
	}
	if accessToken == "" {
		r.dropPending(email)
		session.worker.close()
		return nil, fmt.Errorf("登录完成但未获取有效 access_token，请重试")
	}

	// Ingest: rotate the tokens into the pool (matching by email so legacy
	// dead entries are upgraded in place instead of duplicating).
	result := r.accounts.AddRecoveredAccount(map[string]any{
		"access_token":  accessToken,
		"session_token": sessionToken,
		"email":         session.email,
	})
	r.dropPending(email)
	session.worker.close()
	session.worker.step("账号找回成功: " + session.email)
	return map[string]any{"access_token": result}, nil
}

func (r *AccountRecovery) dropPending(email string) {
	r.mu.Lock()
	delete(r.pending, email)
	r.mu.Unlock()
}

// CancelRecovery discards an in-flight recovery session.
func (r *AccountRecovery) CancelRecovery(email string) {
	email = strings.ToLower(strings.TrimSpace(email))
	r.mu.Lock()
	session := r.pending[email]
	delete(r.pending, email)
	r.mu.Unlock()
	if session != nil {
		session.worker.close()
	}
}

// CleanupExpired drops pending sessions older than 15 minutes.
func (r *AccountRecovery) CleanupExpired() {
	r.mu.Lock()
	var stale []string
	for email, session := range r.pending {
		if time.Since(session.createdAt) > 15*time.Minute {
			stale = append(stale, email)
		}
	}
	r.mu.Unlock()
	for _, email := range stale {
		r.CancelRecovery(email)
	}
}
