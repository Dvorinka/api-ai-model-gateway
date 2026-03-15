package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultProviderTimeout = 10 * time.Second
	maxProviderTimeout     = 30 * time.Second
	maxProviders           = 5
	maxMessages            = 100
)

var (
	ErrNoProviders = errors.New("providers are required")
	ErrNoMessages  = errors.New("messages are required")
)

type Service struct {
	client *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Provider struct {
	Name                string            `json:"name"`
	BaseURL             string            `json:"base_url"`
	APIKey              string            `json:"api_key"`
	Model               string            `json:"model"`
	TimeoutMS           int               `json:"timeout_ms"`
	ChatCompletionsPath string            `json:"chat_completions_path"`
	Headers             map[string]string `json:"headers"`
}

type ChatCompletionRequest struct {
	Messages    []Message  `json:"messages"`
	Providers   []Provider `json:"providers"`
	MaxTokens   *int       `json:"max_tokens,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Attempt struct {
	Provider   string `json:"provider"`
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
}

type ChatCompletionResult struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Message        Message   `json:"message"`
	FinishReason   string    `json:"finish_reason"`
	Usage          Usage     `json:"usage"`
	Attempts       []Attempt `json:"attempts"`
	TotalLatencyMS int64     `json:"total_latency_ms"`
}

type ProviderFailuresError struct {
	Attempts []Attempt
}

func (e *ProviderFailuresError) Error() string {
	return "all providers failed"
}

func NewService() *Service {
	return &Service{
		client: &http.Client{},
	}
}

func (s *Service) ChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResult, error) {
	if err := validateRequest(req); err != nil {
		return ChatCompletionResult{}, err
	}

	started := time.Now()
	attempts := make([]Attempt, 0, len(req.Providers))

	for idx, provider := range req.Providers {
		provider = normalizeProvider(provider, idx)
		attemptStart := time.Now()

		res, statusCode, err := s.callProvider(ctx, provider, req)
		latency := time.Since(attemptStart).Milliseconds()
		if err != nil {
			attempts = append(attempts, Attempt{
				Provider:   provider.Name,
				OK:         false,
				StatusCode: statusCode,
				Error:      err.Error(),
				LatencyMS:  latency,
			})
			continue
		}

		attempts = append(attempts, Attempt{
			Provider:   provider.Name,
			OK:         true,
			StatusCode: statusCode,
			LatencyMS:  latency,
		})
		res.Provider = provider.Name
		res.Attempts = attempts
		res.TotalLatencyMS = time.Since(started).Milliseconds()
		return res, nil
	}

	return ChatCompletionResult{}, &ProviderFailuresError{Attempts: attempts}
}

func validateRequest(req ChatCompletionRequest) error {
	if len(req.Providers) == 0 {
		return ErrNoProviders
	}
	if len(req.Providers) > maxProviders {
		return fmt.Errorf("max %d providers per request", maxProviders)
	}
	if len(req.Messages) == 0 {
		return ErrNoMessages
	}
	if len(req.Messages) > maxMessages {
		return fmt.Errorf("max %d messages per request", maxMessages)
	}
	for _, message := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "system" && role != "user" && role != "assistant" && role != "tool" {
			return errors.New("message role must be system, user, assistant, or tool")
		}
		if strings.TrimSpace(message.Content) == "" {
			return errors.New("message content cannot be empty")
		}
	}

	for idx, provider := range req.Providers {
		if strings.TrimSpace(provider.Model) == "" {
			return fmt.Errorf("providers[%d].model is required", idx)
		}
		base := strings.TrimSpace(provider.BaseURL)
		if base == "" {
			return fmt.Errorf("providers[%d].base_url is required", idx)
		}
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("providers[%d].base_url must be a valid http(s) url", idx)
		}
	}

	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return errors.New("max_tokens must be > 0")
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}

	return nil
}

func normalizeProvider(provider Provider, idx int) Provider {
	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = fmt.Sprintf("provider-%d", idx+1)
	}
	provider.Name = name

	provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if strings.TrimSpace(provider.ChatCompletionsPath) == "" {
		provider.ChatCompletionsPath = "/v1/chat/completions"
	}
	if !strings.HasPrefix(provider.ChatCompletionsPath, "/") {
		provider.ChatCompletionsPath = "/" + provider.ChatCompletionsPath
	}

	timeout := defaultProviderTimeout
	if provider.TimeoutMS > 0 {
		timeout = time.Duration(provider.TimeoutMS) * time.Millisecond
	}
	if timeout > maxProviderTimeout {
		timeout = maxProviderTimeout
	}
	provider.TimeoutMS = int(timeout / time.Millisecond)

	return provider
}

func (s *Service) callProvider(ctx context.Context, provider Provider, req ChatCompletionRequest) (ChatCompletionResult, int, error) {
	body := map[string]any{
		"model":    provider.Model,
		"messages": req.Messages,
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ChatCompletionResult{}, 0, fmt.Errorf("marshal provider request: %w", err)
	}

	providerCtx, cancel := context.WithTimeout(ctx, time.Duration(provider.TimeoutMS)*time.Millisecond)
	defer cancel()

	upstreamReq, err := http.NewRequestWithContext(providerCtx, http.MethodPost, provider.BaseURL+provider.ChatCompletionsPath, bytes.NewReader(payload))
	if err != nil {
		return ChatCompletionResult{}, 0, fmt.Errorf("build provider request: %w", err)
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(provider.APIKey); key != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+key)
	}
	for headerKey, headerValue := range provider.Headers {
		if strings.TrimSpace(headerKey) == "" {
			continue
		}
		upstreamReq.Header.Set(headerKey, headerValue)
	}

	upstreamResp, err := s.client.Do(upstreamReq)
	if err != nil {
		return ChatCompletionResult{}, 0, fmt.Errorf("request failed: %w", err)
	}
	defer upstreamResp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(upstreamResp.Body, 1<<20))
	if err != nil {
		return ChatCompletionResult{}, upstreamResp.StatusCode, errors.New("failed to read provider response")
	}

	if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(responseBody))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = "provider returned non-2xx response"
		}
		return ChatCompletionResult{}, upstreamResp.StatusCode, fmt.Errorf("upstream error: %s", msg)
	}

	var providerPayload struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &providerPayload); err != nil {
		return ChatCompletionResult{}, upstreamResp.StatusCode, errors.New("provider returned invalid json")
	}
	if len(providerPayload.Choices) == 0 {
		return ChatCompletionResult{}, upstreamResp.StatusCode, errors.New("provider response missing choices")
	}
	if strings.TrimSpace(providerPayload.Choices[0].Message.Content) == "" {
		return ChatCompletionResult{}, upstreamResp.StatusCode, errors.New("provider response missing assistant content")
	}

	model := strings.TrimSpace(providerPayload.Model)
	if model == "" {
		model = provider.Model
	}
	assistantRole := strings.TrimSpace(providerPayload.Choices[0].Message.Role)
	if assistantRole == "" {
		assistantRole = "assistant"
	}

	return ChatCompletionResult{
		ID:    providerPayload.ID,
		Model: model,
		Message: Message{
			Role:    assistantRole,
			Content: providerPayload.Choices[0].Message.Content,
		},
		FinishReason: providerPayload.Choices[0].FinishReason,
		Usage:        providerPayload.Usage,
	}, upstreamResp.StatusCode, nil
}
