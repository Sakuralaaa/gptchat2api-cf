package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type solverAwareLookup struct {
	testAccountLookup
	solverURL string
	solverKey string
}

func (l solverAwareLookup) SentinelSolverConfig() (string, string) {
	return l.solverURL, l.solverKey
}

func TestSentinelSolverClientHealthAndTurnstile(t *testing.T) {
	var mu sync.Mutex
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ready":true,"user_agent":"ua-solver","timezone":"America/Phoenix","timezone_offset_min":420,"timezone_long_name":"Mountain Standard Time"}`))
		case "/v1/turnstile":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["dx"] != "dx-1" || body["p"] != "p-1" {
				t.Errorf("turnstile body = %#v", body)
			}
			if body["requirements"] == nil {
				t.Errorf("turnstile body is missing the prepare response: %#v", body)
			}
			_, _ = w.Write([]byte(`{"token":"turnstile-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	solver := sentinelSolverClientFor(server.URL, "secret")
	if solver == nil {
		t.Fatal("sentinelSolverClientFor() returned nil")
	}
	if got := solver.UserAgent(context.Background()); got != "ua-solver" {
		t.Fatalf("UserAgent() = %q", got)
	}
	name, offsetMin, longName, ok := solver.TimezoneInfo(context.Background())
	if !ok || name != "America/Phoenix" || offsetMin != 420 || longName != "Mountain Standard Time" {
		t.Fatalf("TimezoneInfo() = %q, %d, %q, %v", name, offsetMin, longName, ok)
	}
	token, err := solver.SolveTurnstile(context.Background(), "dx-1", "p-1", map[string]any{"prepare_token": "prep-1"})
	if err != nil || token != "turnstile-1" {
		t.Fatalf("SolveTurnstile() = %q, %v", token, err)
	}
	mu.Lock()
	gotAuth := auth
	mu.Unlock()
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
}

// TestGetChatRequirementsUsesSolverEnvironment drives the two step handshake
// against a stub chatgpt.com and a stub solver, and checks the invariants the
// current sentinel validation depends on: the requirements token encodes the
// solver page UA, the solver receives the same `p` that was submitted (it is
// the challenge XOR key), and the conversation timezone follows the solver.
func TestGetChatRequirementsUsesSolverEnvironment(t *testing.T) {
	const solverUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

	var mu sync.Mutex
	var prepareP, solverP, solverDX string

	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ready":true,"user_agent":"` + solverUA + `","timezone":"America/Phoenix","timezone_offset_min":420,"timezone_long_name":"Mountain Standard Time"}`))
		case "/v1/turnstile":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			solverP, _ = body["p"].(string)
			solverDX, _ = body["dx"].(string)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"token":"turnstile-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer solver.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/sentinel/chat-requirements/prepare":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			prepareP, _ = body["p"].(string)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"prepare_token":"prep-token","proofofwork":{"required":true,"seed":"seed","difficulty":"0fffff"},"turnstile":{"required":true,"dx":"dx-1"},"arkose":{"required":false}}`))
		case "/backend-api/sentinel/chat-requirements/finalize":
			_, _ = w.Write([]byte(`{"token":"req-token","so_token":"so-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer chat.Close()

	client := &Client{
		BaseURL:     chat.URL,
		AccessToken: "token-1",
		httpClient:  chat.Client(),
		lookup: solverAwareLookup{
			testAccountLookup: testAccountLookup{"token-1": {"chatgpt_account_id": "acct-1"}},
			solverURL:         solver.URL,
		},
	}
	client.fp = client.buildFingerprint()
	client.applyBrowserFingerprint()
	client.userAgent = client.fp["user-agent"]

	reqs, err := client.getChatRequirements(context.Background())
	if err != nil {
		t.Fatalf("getChatRequirements() error = %v", err)
	}
	if reqs.Token != "req-token" || reqs.TurnstileToken != "turnstile-token" || reqs.SOToken != "so-token" {
		t.Fatalf("requirements = %#v", reqs)
	}
	if reqs.ProofToken == "" || !strings.HasSuffix(reqs.ProofToken, "~S") {
		t.Fatalf("proof token = %q", reqs.ProofToken)
	}

	mu.Lock()
	gotPrepareP, gotSolverP, gotDX := prepareP, solverP, solverDX
	mu.Unlock()
	if gotPrepareP == "" || gotPrepareP != gotSolverP {
		t.Fatalf("prepare p = %q, solver p = %q", gotPrepareP, gotSolverP)
	}
	if gotDX != "dx-1" {
		t.Fatalf("solver dx = %q", gotDX)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(gotPrepareP, "gAAAAAC"))
	if err != nil {
		t.Fatalf("decode p: %v", err)
	}
	var config []any
	if err := json.Unmarshal(decoded, &config); err != nil {
		t.Fatalf("unmarshal p: %v", err)
	}
	if len(config) != 25 || config[4] != solverUA {
		t.Fatalf("p[4] = %#v, want the solver user agent", config[4])
	}

	if got := client.conversationTimezoneName(); got != "America/Phoenix" {
		t.Fatalf("conversationTimezoneName() = %q", got)
	}
	if got := client.conversationTimezoneOffsetMin(); got != 420 {
		t.Fatalf("conversationTimezoneOffsetMin() = %d", got)
	}
}
