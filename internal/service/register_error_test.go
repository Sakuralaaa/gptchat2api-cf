package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type registerErrorRoundTripFunc func(*http.Request) (*http.Response, error)

func (f registerErrorRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func registerErrorJSONResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func testRegisterWorker(transport http.RoundTripper) (*registerWorker, *RegisterService) {
	service := &RegisterService{subscribers: map[chan string]struct{}{}}
	return &registerWorker{
		service:  service,
		index:    1,
		client:   &http.Client{Transport: transport},
		deviceID: "device-1",
	}, service
}

func TestPlatformAuthorizeIncludesUpstreamErrorDetail(t *testing.T) {
	worker, _ := testRegisterWorker(registerErrorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/accounts/authorize" {
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}
		return registerErrorJSONResponse(req, http.StatusForbidden, `{"error":{"code":"country_blocked","message":"not allowed"}}`), nil
	}))

	err := worker.platformAuthorize(context.Background(), "user@example.test")
	if err == nil {
		t.Fatal("platformAuthorize() returned nil error")
	}
	if got := err.Error(); !strings.Contains(got, "platform_authorize_http_403: country_blocked - not allowed") {
		t.Fatalf("platformAuthorize() error = %q", got)
	}
}

func TestRegisterUserIncludesResponseDetailAndDomainHint(t *testing.T) {
	worker, service := testRegisterWorker(registerErrorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/backend-api/sentinel/req":
			return registerErrorJSONResponse(req, http.StatusOK, `{"token":"challenge-token","proofofwork":{"required":false}}`), nil
		case "/api/accounts/user/register":
			if req.Header.Get("openai-sentinel-token") == "" {
				t.Fatal("register request did not include sentinel token")
			}
			return registerErrorJSONResponse(req, http.StatusBadRequest, `{"message":"Failed to create account. Please try again.","request_id":"req_1"}`), nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	}))

	err := worker.registerUser(context.Background(), "user@example.test", "password")
	if err == nil {
		t.Fatal("registerUser() returned nil error")
	}
	if got := err.Error(); !strings.Contains(got, "user_register_http_400, detail=") || !strings.Contains(got, "Failed to create account") {
		t.Fatalf("registerUser() error = %q", got)
	}
	if !registerLogsContain(service.logs, "邮箱域名很可能因滥用被封禁") {
		t.Fatalf("logs did not contain domain-block hint: %#v", service.logs)
	}
}

func TestCreateAccountIncludesResponseDetailAndDomainHint(t *testing.T) {
	worker, service := testRegisterWorker(registerErrorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/backend-api/sentinel/req":
			return registerErrorJSONResponse(req, http.StatusOK, `{"token":"challenge-token","proofofwork":{"required":false}}`), nil
		case "/api/accounts/create_account":
			if req.Header.Get("openai-sentinel-token") == "" {
				t.Fatal("create account request did not include sentinel token")
			}
			return registerErrorJSONResponse(req, http.StatusBadRequest, `{"message":"Failed to create account. Please try again.","request_id":"req_2"}`), nil
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	}))

	err := worker.createAccount(context.Background(), "Test User", "1990-01-01")
	if err == nil {
		t.Fatal("createAccount() returned nil error")
	}
	if got := err.Error(); !strings.Contains(got, "create_account_http_400, detail=") || !strings.Contains(got, "Failed to create account") {
		t.Fatalf("createAccount() error = %q", got)
	}
	if !registerLogsContain(service.logs, "邮箱域名很可能因滥用被封禁") {
		t.Fatalf("logs did not contain domain-block hint: %#v", service.logs)
	}
}

func registerLogsContain(logs []map[string]any, text string) bool {
	for _, item := range logs {
		if strings.Contains(toString(item["text"]), text) {
			return true
		}
	}
	return false
}
