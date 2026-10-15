package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SentinelSolverProvider is an optional capability of the account lookup. When
// present, the backend asks a resident-browser solver service (sentinel_solver/)
// to compute Turnstile tokens inside a real chatgpt.com page instead of
// simulating the challenge VM in-process.
type SentinelSolverProvider interface {
	SentinelSolverConfig() (url string, key string)
}

const (
	// A healthy solver is cached for a while; a broken one only briefly, so we
	// do not probe a dead service on every single request.
	sentinelSolverHealthTTL      = 10 * time.Minute
	sentinelSolverHealthErrorTTL = 30 * time.Second
	sentinelSolverRequestTimeout = 40 * time.Second
)

type sentinelSolverHealth struct {
	Ready             bool   `json:"ready"`
	UserAgent         string `json:"user_agent"`
	Timezone          string `json:"timezone"`
	TimezoneOffsetMin *int   `json:"timezone_offset_min"`
	TimezoneLongName  string `json:"timezone_long_name"`
	LastError         string `json:"last_error"`
}

type sentinelSolverClient struct {
	baseURL string
	key     string
	http    *http.Client

	mu        sync.Mutex
	health    sentinelSolverHealth
	expiresAt time.Time
}

var (
	sentinelSolverClientsMu sync.Mutex
	sentinelSolverClients   = map[string]*sentinelSolverClient{}
)

// sentinelSolverClientFor returns a shared client for baseURL, or nil when no
// solver is configured. Changing the key invalidates the cached client.
func sentinelSolverClientFor(baseURL, key string) *sentinelSolverClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	sentinelSolverClientsMu.Lock()
	defer sentinelSolverClientsMu.Unlock()
	if client, ok := sentinelSolverClients[baseURL]; ok && client.key == key {
		return client
	}
	client := &sentinelSolverClient{
		baseURL: baseURL,
		key:     key,
		http:    &http.Client{Timeout: sentinelSolverRequestTimeout},
	}
	sentinelSolverClients[baseURL] = client
	return client
}

func (s *sentinelSolverClient) do(ctx context.Context, method, path string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func sentinelSolverSnippet(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) > 200 {
		return text[:200]
	}
	return text
}

// healthInfo returns the solver page state, refreshing it when stale. The
// returned error is informational: callers fall back to in-process behaviour.
func (s *sentinelSolverClient) healthInfo(ctx context.Context) (sentinelSolverHealth, error) {
	s.mu.Lock()
	if time.Now().Before(s.expiresAt) {
		cached := s.health
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	data, status, err := s.do(ctx, http.MethodGet, "/health", nil)
	health := sentinelSolverHealth{}
	switch {
	case err != nil:
	case status < 200 || status >= 300:
		err = fmt.Errorf("sentinel solver health: status=%d body=%s", status, sentinelSolverSnippet(data))
	default:
		err = json.Unmarshal(data, &health)
	}
	if err != nil || strings.TrimSpace(health.UserAgent) == "" {
		if err == nil {
			err = fmt.Errorf("sentinel solver health returned an empty user agent")
		}
		health = sentinelSolverHealth{LastError: err.Error()}
		s.mu.Lock()
		s.health = health
		s.expiresAt = time.Now().Add(sentinelSolverHealthErrorTTL)
		s.mu.Unlock()
		return health, err
	}

	s.mu.Lock()
	s.health = health
	s.expiresAt = time.Now().Add(sentinelSolverHealthTTL)
	s.mu.Unlock()
	return health, nil
}

// UserAgent returns the UA of the solver page. Requirements/proof tokens must
// encode the same UA as the page that computed the Turnstile token, otherwise
// the conversation endpoint answers with a disguised "Model not found" 404.
func (s *sentinelSolverClient) UserAgent(ctx context.Context) string {
	health, _ := s.healthInfo(ctx)
	return strings.TrimSpace(health.UserAgent)
}

// Timezone returns the IANA timezone of the solver page, which is expected to
// match the geolocation of the outbound proxy.
func (s *sentinelSolverClient) Timezone(ctx context.Context) string {
	health, _ := s.healthInfo(ctx)
	return strings.TrimSpace(health.Timezone)
}

// TimezoneInfo returns the solver page timezone as its IANA name, its
// JS getTimezoneOffset() value in minutes, and its long display name.
func (s *sentinelSolverClient) TimezoneInfo(ctx context.Context) (string, int, string, bool) {
	health, err := s.healthInfo(ctx)
	if err != nil || health.TimezoneOffsetMin == nil || strings.TrimSpace(health.Timezone) == "" {
		return "", 0, "", false
	}
	return strings.TrimSpace(health.Timezone), *health.TimezoneOffsetMin, strings.TrimSpace(health.TimezoneLongName), true
}

func (s *sentinelSolverClient) SolveTurnstile(ctx context.Context, dx, p string) (string, error) {
	data, status, err := s.do(ctx, http.MethodPost, "/v1/turnstile", map[string]any{"dx": dx, "p": p})
	if err != nil {
		return "", fmt.Errorf("sentinel solver turnstile: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("sentinel solver turnstile: status=%d body=%s", status, sentinelSolverSnippet(data))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("sentinel solver turnstile: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return "", fmt.Errorf("sentinel solver turnstile returned an empty token")
	}
	return payload.Token, nil
}

func (s *sentinelSolverClient) SolveSO(ctx context.Context, collectorDX, snapshotDX, p string) (string, error) {
	payload := map[string]any{"collector_dx": collectorDX, "snapshot_dx": snapshotDX, "p": p}
	data, status, err := s.do(ctx, http.MethodPost, "/v1/so", payload)
	if err != nil {
		return "", fmt.Errorf("sentinel solver so: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("sentinel solver so: status=%d body=%s", status, sentinelSolverSnippet(data))
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("sentinel solver so: %w", err)
	}
	return strings.TrimSpace(parsed.Token), nil
}

// sentinelSolver returns the configured solver client, or nil when the account
// lookup exposes none or no solver URL is configured.
func (c *Client) sentinelSolver() *sentinelSolverClient {
	provider, ok := c.lookup.(SentinelSolverProvider)
	if !ok || provider == nil {
		return nil
	}
	baseURL, key := provider.SentinelSolverConfig()
	return sentinelSolverClientFor(baseURL, key)
}

// sentinelUserAgent returns the UA that requirements/proof tokens must encode:
// the UA of the page that solved the Turnstile challenge.
func (c *Client) sentinelUserAgent(ctx context.Context) string {
	if solver := c.sentinelSolver(); solver != nil {
		if userAgent := solver.UserAgent(ctx); userAgent != "" {
			return userAgent
		}
	}
	return c.userAgent
}

// solveTurnstileChallenge computes the Turnstile token for the challenge
// program dx. It prefers the resident-browser solver service and falls back to
// the in-process VM, which lags behind newer challenge programs.
func (c *Client) solveTurnstileChallenge(ctx context.Context, dx, p string) string {
	if solver := c.sentinelSolver(); solver != nil {
		if token, err := solver.SolveTurnstile(ctx, dx, p); err == nil {
			return token
		}
	}
	return solveTurnstileToken(dx, p)
}

// powTimezoneInfo resolves the timezone embedded in sentinel tokens and
// declared on conversation requests, reporting whether the solver answered.
func (c *Client) powTimezoneInfo(ctx context.Context) (powTimezone, bool) {
	if solver := c.sentinelSolver(); solver != nil {
		if name, offsetMin, longName, ok := solver.TimezoneInfo(ctx); ok {
			return powTimezone{Name: name, OffsetMin: offsetMin, LongName: longName}, true
		}
	}
	return defaultPowTimezone(), false
}

// powTimezone resolves the timezone embedded in sentinel tokens and declared on
// conversation requests. The outbound proxy's geolocation and the declared
// timezone have to agree, so the solver page timezone (configured to match the
// proxy exit) wins whenever it is available.
func (c *Client) powTimezone(ctx context.Context) powTimezone {
	resolved, _ := c.powTimezoneInfo(ctx)
	return resolved
}

// conversationTimezoneName returns the IANA timezone the conversation payload
// must declare. It tracks the solver page, which is configured to match the
// outbound proxy's geolocation, and falls back to the built-in default.
func (c *Client) conversationTimezoneName() string {
	return c.cachedTimezone().Name
}

// conversationTimezoneOffsetMin returns the matching JS getTimezoneOffset()
// value for the conversation payload.
func (c *Client) conversationTimezoneOffsetMin() int {
	return c.cachedTimezone().OffsetMin
}

// cachedTimezone resolves the solver page timezone at most once per health
// cache window, so payload builders without a request context can use it.
func (c *Client) cachedTimezone() powTimezone {
	c.timezoneMu.Lock()
	defer c.timezoneMu.Unlock()
	if time.Now().Before(c.timezoneExpiresAt) {
		return c.timezone
	}
	resolved, ok := c.powTimezoneInfo(context.Background())
	ttl := sentinelSolverHealthTTL
	if !ok {
		ttl = sentinelSolverHealthErrorTTL
	}
	c.timezone = resolved
	c.timezoneExpiresAt = time.Now().Add(ttl)
	return resolved
}
