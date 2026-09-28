package service

// Sentinel token generation via OpenAI's real sdk.js executed in a Node
// subprocess (QuickJS runtime embedded in Node). This is a faithful Go port of
// gpt-auto-register's sentinel_quickjs.py: requirements -> /sentinel/req
// challenge -> solve, with the server-decided SO token semantics.
//
// The quickjs script and the environment payload are kept byte-compatible with
// the working Python implementation so behaviour matches exactly.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

//go:embed openai_sentinel_quickjs.js
var registerSentinelQuickJSScript []byte

const (
	// Matches sentinel_quickjs.py SENTINEL_VERSION / SENTINEL_SDK_URL.
	registerSentinelNodeVersion  = "20260219f9f6"
	registerSentinelNodeSDKURL   = "https://sentinel.openai.com/sentinel/" + registerSentinelNodeVersion + "/sdk.js"
	registerSentinelNodeCacheDir = "openai-sentinel-demo"

	registerSentinelDefaultTimeout = 45 * time.Second
)

func registerNodeBinary() string {
	if p := strings.TrimSpace(os.Getenv("OPENAI_SENTINEL_NODE_PATH")); p != "" {
		return p
	}
	if _, err := exec.LookPath("node"); err == nil {
		return "node"
	}
	return ""
}

// ensureSentinelSDKFile downloads OpenAI's sdk.js once into the temp dir and
// returns its path. Safe for concurrent workers (atomic rename).
func (w *registerWorker) ensureSentinelSDKFile(ctx context.Context) (string, error) {
	cacheDir := filepath.Join(os.TempDir(), registerSentinelNodeCacheDir, registerSentinelNodeVersion)
	sdkFile := filepath.Join(cacheDir, "sdk.js")
	if info, err := os.Stat(sdkFile); err == nil && info.Size() > 0 {
		return sdkFile, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registerSentinelNodeSDKURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "zh-CN,zh;q=0.9")
	req.Header.Set("referer", "https://auth.openai.com/")
	req.Header.Set("sec-fetch-dest", "script")
	req.Header.Set("sec-fetch-mode", "no-cors")
	req.Header.Set("sec-fetch-site", "same-site")
	req.Header.Set("User-Agent", w.flowUA())
	resp, err := w.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download sdk.js failed: HTTP %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", fmt.Errorf("download sdk.js failed: empty response")
	}
	tmp := sdkFile + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, sdkFile); err != nil {
		_ = os.Remove(tmp)
		// Another worker may have completed the download concurrently.
		if info, statErr := os.Stat(sdkFile); statErr == nil && info.Size() > 0 {
			return sdkFile, nil
		}
		return "", err
	}
	return sdkFile, nil
}

// runSentinelQuickJSAction pipes a JSON payload through the embedded quickjs
// script executed by node and parses the JSON result from stdout.
func runSentinelQuickJSAction(nodeBin, sdkFile string, action string, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	script, err := os.CreateTemp("", "openai-sentinel-*.js")
	if err != nil {
		return nil, err
	}
	scriptPath := script.Name()
	defer os.Remove(scriptPath)
	if _, err := script.Write(registerSentinelQuickJSScript); err != nil {
		script.Close()
		return nil, err
	}
	if err := script.Close(); err != nil {
		return nil, err
	}

	body := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		body[k] = v
	}
	body["action"] = action
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBin, scriptPath)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Env = append(os.Environ(), "OPENAI_SENTINEL_SDK_FILE="+sdkFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = "unknown"
		}
		return nil, fmt.Errorf("QuickJS 执行失败: %s", truncateSentinelDetail(detail))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return nil, fmt.Errorf("QuickJS 返回空输出")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return nil, fmt.Errorf("QuickJS 输出不是 JSON 对象")
	}
	return decoded, nil
}

func truncateSentinelDetail(text string) string {
	if len(text) <= 300 {
		return text
	}
	return text[:300]
}

func (w *registerWorker) sentinelEnvironmentPayload() map[string]any {
	screenW, screenH := "1920", "1080"
	if parts := strings.SplitN(w.fp.screen, "x", 2); len(parts) == 2 {
		screenW, screenH = parts[0], parts[1]
	}
	return map[string]any{
		"device_id":         w.deviceID,
		"user_agent":        w.flowUA(),
		"screen_width":      screenW,
		"screen_height":     screenH,
		"language":          "en-US",
		"languages":         []string{"en-US"},
		"platform":          "Win32",
		"vendor":            "Google Inc.",
		"hardware_concurrency": 8,
		"device_pixel_ratio":   1.0,
		"max_touch_points":     0,
		"timezone":             "UTC",
	}
}

func (w *registerWorker) sentinelChallenge(ctx context.Context, flow, requestP string) (map[string]any, error) {
	payload := map[string]any{"p": requestP, "id": w.deviceID, "flow": flow}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Content-Type":   "text/plain;charset=UTF-8",
		"Origin":         registerSentinelBase,
		"Referer":        registerSentinelBase + "/backend-api/sentinel/frame.html?sv=" + registerSentinelNodeVersion,
		"User-Agent":     w.flowUA(),
		"sec-ch-ua":      w.fp.secChUA,
		"sec-ch-ua-mobile": "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-dest":   "empty",
		"sec-fetch-mode":   "cors",
		"sec-fetch-site":   "same-origin",
	}
	status, resp, err := w.requestRawJSON(ctx, http.MethodPost, registerSentinelBase+"/backend-api/sentinel/req", data, headers)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("/sentinel/req HTTP %d", status)
	}
	return resp, nil
}

// buildSentinelArtifactsNode mirrors get_sentinel_token_via_quickjs: it returns
// the SDK token plus the SO token whenever the server requires one. An empty SO
// token is only an error when challenge.so.required is true.
func (w *registerWorker) buildSentinelArtifactsNode(ctx context.Context, flow string) (registerSentinelArtifacts, error) {
	nodeBin := registerNodeBinary()
	if nodeBin == "" {
		return registerSentinelArtifacts{}, fmt.Errorf("node binary not available")
	}
	sdkFile, err := w.ensureSentinelSDKFile(ctx)
	if err != nil {
		return registerSentinelArtifacts{}, err
	}
	env := w.sentinelEnvironmentPayload()

	requirements, err := runSentinelQuickJSAction(nodeBin, sdkFile, "requirements", env, registerSentinelDefaultTimeout)
	if err != nil {
		return registerSentinelArtifacts{}, err
	}
	requestP := strings.TrimSpace(util.Clean(requirements["request_p"]))
	if requestP == "" {
		return registerSentinelArtifacts{}, fmt.Errorf("sentinel requirements returned no request_p")
	}

	challenge, err := w.sentinelChallenge(ctx, flow, requestP)
	if err != nil {
		return registerSentinelArtifacts{}, err
	}
	challengeToken := strings.TrimSpace(util.Clean(challenge["token"]))
	if challengeToken == "" {
		return registerSentinelArtifacts{}, fmt.Errorf("sentinel challenge token empty")
	}

	solvePayload := make(map[string]any, len(env)+4)
	for k, v := range env {
		solvePayload[k] = v
	}
	solvePayload["request_p"] = requestP
	solvePayload["challenge"] = challenge
	solvePayload["flow"] = flow
	solvePayload["behavior_duration_ms"] = 4200

	solved, err := runSentinelQuickJSAction(nodeBin, sdkFile, "solve", solvePayload, registerSentinelDefaultTimeout)
	if err != nil {
		return registerSentinelArtifacts{}, err
	}

	token := strings.TrimSpace(util.Clean(solved["token"]))
	soToken := strings.TrimSpace(util.Clean(solved["so_token"]))
	soRequired := false
	if soMap, ok := challenge["so"].(map[string]any); ok {
		soRequired = util.ToBool(soMap["required"])
	}
	if token == "" {
		return registerSentinelArtifacts{}, fmt.Errorf("sentinel sdk token empty")
	}
	if soRequired && soToken == "" {
		return registerSentinelArtifacts{}, fmt.Errorf("sentinel requires SO token but solve returned none")
	}
	return registerSentinelArtifacts{token: token, soToken: soToken}, nil
}

// buildSentinelArtifactsWithFallback prefers the Node/QuickJS path (identical to
// the working Python implementation) and falls back to the pure-Go generator if
// Node is unavailable.
func (w *registerWorker) buildSentinelArtifactsWithFallback(ctx context.Context, flow string) (registerSentinelArtifacts, error) {
	if registerNodeBinary() != "" {
		artifacts, err := w.buildSentinelArtifactsNode(ctx, flow)
		if err == nil {
			return artifacts, nil
		}
		w.step(fmt.Sprintf("Node Sentinel 失败，回退内置 PoW 生成器: %v", err))
	}
	return w.buildSentinelArtifacts(ctx, flow)
}
