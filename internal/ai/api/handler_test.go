package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apiservices/ai-model-gateway/internal/ai/gateway"
)

func TestChatCompletionsEndpoint(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-handler",
			"model":"mock-model",
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}
		}`))
	}))
	defer upstream.Close()

	h := NewHandler(gateway.NewService())
	body := `{
	  "messages":[{"role":"user","content":"ping"}],
	  "providers":[{"name":"mock","base_url":"` + upstream.URL + `","model":"mock-model"}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/ai/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"provider":"mock"`) {
		t.Fatalf("expected response to include provider name, got: %s", rr.Body.String())
	}
}

func TestChatCompletionsEndpointBadRequest(t *testing.T) {
	t.Parallel()

	h := NewHandler(gateway.NewService())
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/chat/completions", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}
