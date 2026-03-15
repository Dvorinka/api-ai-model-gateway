package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletionFallbackSuccess(t *testing.T) {
	t.Parallel()

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"temporary outage"}`))
	}))
	defer failServer.Close()

	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer backup-key" {
			t.Fatalf("expected auth header, got %q", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}
		if payload["model"] != "model-b" {
			t.Fatalf("expected model-b, got %v", payload["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"model-b",
			"choices":[{"message":{"role":"assistant","content":"fallback worked"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}
		}`))
	}))
	defer successServer.Close()

	svc := NewService()
	maxTokens := 120
	temperature := 0.2

	res, err := svc.ChatCompletion(context.Background(), ChatCompletionRequest{
		Messages:    []Message{{Role: "user", Content: "hello"}},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		Providers: []Provider{
			{Name: "primary", BaseURL: failServer.URL, Model: "model-a"},
			{Name: "backup", BaseURL: successServer.URL, APIKey: "backup-key", Model: "model-b"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}

	if res.Provider != "backup" {
		t.Fatalf("expected backup provider, got %s", res.Provider)
	}
	if res.Message.Content != "fallback worked" {
		t.Fatalf("unexpected assistant output: %s", res.Message.Content)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(res.Attempts))
	}
	if res.Attempts[0].OK {
		t.Fatal("expected first attempt to fail")
	}
	if !res.Attempts[1].OK {
		t.Fatal("expected second attempt to succeed")
	}
}

func TestChatCompletionAllProvidersFail(t *testing.T) {
	t.Parallel()

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"gateway"}`))
	}))
	defer failServer.Close()

	svc := NewService()
	_, err := svc.ChatCompletion(context.Background(), ChatCompletionRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Providers: []Provider{
			{Name: "p1", BaseURL: failServer.URL, Model: "model-a"},
			{Name: "p2", BaseURL: failServer.URL, Model: "model-b"},
		},
	})
	if err == nil {
		t.Fatal("expected error but got nil")
	}

	providerErr, ok := err.(*ProviderFailuresError)
	if !ok {
		t.Fatalf("expected ProviderFailuresError, got %T", err)
	}
	if len(providerErr.Attempts) != 2 {
		t.Fatalf("expected 2 failed attempts, got %d", len(providerErr.Attempts))
	}
	if providerErr.Attempts[0].OK || providerErr.Attempts[1].OK {
		t.Fatal("all attempts should be failed")
	}
}
