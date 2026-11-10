package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simpleagent/internal/config"
	"simpleagent/internal/llm"
)

func testConfig() config.LLMConfig {
	return config.LLMConfig{
		BaseURL:         "https://api.example.com/v1",
		APIKey:          "sk-test",
		Model:           "test-model",
		Temperature:     0,
		MaxTokens:       128,
		TimeoutSeconds:  5,
		ReasoningEffort: "none",
	}
}

func newTestClient(t *testing.T, cfg config.LLMConfig, handler http.HandlerFunc) (*llm.Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg.BaseURL = server.URL + "/v1"
	if cfg.Model == "" {
		cfg.Model = "test-model"
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 5
	}
	client, err := llm.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func successBody(content string) map[string]any {
	return map[string]any{
		"id":    "chatcmpl-1",
		"model": "test-model",
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content, "reasoning_content": "thinking"},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":             10,
			"completion_tokens":         2,
			"total_tokens":              12,
			"prompt_cache_hit_tokens":   3,
			"prompt_cache_miss_tokens":  7,
			"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
		},
	}
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.LLMConfig)
		want   string
	}{
		{"empty base url", func(c *config.LLMConfig) { c.BaseURL = "" }, "base_url"},
		{"invalid scheme", func(c *config.LLMConfig) { c.BaseURL = "ftp://example.com/v1" }, "http(s)"},
		{"userinfo", func(c *config.LLMConfig) { c.BaseURL = "https://user:pass@example.com/v1" }, "userinfo"},
		{"query", func(c *config.LLMConfig) { c.BaseURL = "https://example.com/v1?x=1" }, "query"},
		{"fragment", func(c *config.LLMConfig) { c.BaseURL = "https://example.com/v1#x" }, "fragment"},
		{"endpoint already included", func(c *config.LLMConfig) { c.BaseURL = "https://example.com/v1/chat/completions" }, "/chat/completions"},
		{"empty model", func(c *config.LLMConfig) { c.Model = "" }, "model"},
		{"zero timeout", func(c *config.LLMConfig) { c.TimeoutSeconds = 0 }, "timeout_seconds"},
		{"negative temperature", func(c *config.LLMConfig) { c.Temperature = -0.1 }, "temperature"},
		{"temperature too high", func(c *config.LLMConfig) { c.Temperature = 2.1 }, "temperature"},
		{"negative max tokens", func(c *config.LLMConfig) { c.MaxTokens = -1 }, "max_tokens"},
		{"invalid reasoning effort", func(c *config.LLMConfig) { c.ReasoningEffort = "extreme" }, "reasoning_effort"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			tt.mutate(&cfg)
			if _, err := llm.New(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	if _, err := llm.New(testConfig()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestCompleteRequestAndResult(t *testing.T) {
	var got struct {
		Model           string        `json:"model"`
		Messages        []llm.Message `json:"messages"`
		Temperature     float64       `json:"temperature"`
		MaxTokens       *int          `json:"max_tokens"`
		ReasoningEffort *string       `json:"reasoning_effort"`
	}

	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, successBody("hello"))
	})

	messages := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "question"},
	}
	result, err := client.CompleteResult(context.Background(), messages)
	if err != nil {
		t.Fatalf("CompleteResult: %v", err)
	}

	if got.Model != "test-model" {
		t.Errorf("model = %q", got.Model)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "question" {
		t.Errorf("messages = %+v", got.Messages)
	}
	if got.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", got.Temperature)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 128 {
		t.Errorf("max_tokens = %v, want 128", got.MaxTokens)
	}
	if got.ReasoningEffort == nil || *got.ReasoningEffort != "none" {
		t.Errorf("reasoning_effort = %v, want none", got.ReasoningEffort)
	}

	if result.Content != "hello" || result.ReasoningContent != "thinking" {
		t.Errorf("result content/reasoning = %q/%q", result.Content, result.ReasoningContent)
	}
	if result.ID != "chatcmpl-1" || result.Model != "test-model" || result.FinishReason != "stop" {
		t.Errorf("result metadata = %+v", result)
	}
	if result.Usage.PromptTokens != 10 || result.Usage.CompletionTokens != 2 || result.Usage.TotalTokens != 12 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if result.Usage.PromptCacheHitTokens != 3 || result.Usage.PromptCacheMissTokens != 7 {
		t.Errorf("cache usage = %+v", result.Usage)
	}
	if result.Usage.CompletionTokensDetails == nil || result.Usage.CompletionTokensDetails.ReasoningTokens != 1 {
		t.Errorf("reasoning usage = %+v", result.Usage.CompletionTokensDetails)
	}
}

func TestCompleteWrapper(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":    "chatcmpl-2",
			"model": "test-model",
			"choices": []any{
				map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "answer"},
					"finish_reason": "stop",
				},
			},
		})
	})

	content, err := client.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if content != "answer" {
		t.Fatalf("content = %q, want answer", content)
	}
}

func TestNoAPIKey(t *testing.T) {
	cfg := testConfig()
	cfg.APIKey = ""
	cfg.APIKeyEnv = ""

	client, _ := newTestClient(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		writeJSON(w, http.StatusOK, successBody("ok"))
	})

	if _, err := client.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestEnvironmentAPIKey(t *testing.T) {
	t.Setenv("TEST_LLM_API_KEY", "sk-from-env")
	cfg := testConfig()
	cfg.APIKey = ""
	cfg.APIKeyEnv = "TEST_LLM_API_KEY"

	client, _ := newTestClient(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-from-env" {
			t.Errorf("Authorization = %q, want env key", got)
		}
		writeJSON(w, http.StatusOK, successBody("ok"))
	})

	if _, err := client.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestMaxTokensAndReasoningEffortOmitted(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTokens = 0
	cfg.ReasoningEffort = ""

	client, _ := newTestClient(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if _, ok := body["max_tokens"]; ok {
			t.Error("max_tokens present, want omitted")
		}
		if _, ok := body["reasoning_effort"]; ok {
			t.Error("reasoning_effort present, want omitted")
		}
		if _, ok := body["temperature"]; !ok {
			t.Error("temperature missing, want present")
		}
		writeJSON(w, http.StatusOK, successBody("ok"))
	})

	if _, err := client.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestHTTPError(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]any{
				"message": "Incorrect API key provided",
				"type":    "invalid_request_error",
			},
		})
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("CompleteResult error = nil, want HTTPError")
	}
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *llm.HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d", httpErr.StatusCode)
	}
	if httpErr.Message != "Incorrect API key provided" {
		t.Errorf("Message = %q", httpErr.Message)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("error leaked API key: %v", err)
	}
}

func TestEmptyChoices(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{}})
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrEmptyChoices) {
		t.Fatalf("error = %v, want ErrEmptyChoices", err)
	}
}

func TestEmptyContent(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{
				map[string]any{
					"message":       map[string]any{"role": "assistant", "content": ""},
					"finish_reason": "length",
				},
			},
		})
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrEmptyContent) {
		t.Fatalf("error = %v, want ErrEmptyContent", err)
	}
	if !strings.Contains(err.Error(), "finish_reason=length") {
		t.Fatalf("error = %v, want finish_reason", err)
	}
}

func TestInvalidJSON(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrDecode) {
		t.Fatalf("error = %v, want ErrDecode", err)
	}
}

func TestContextCanceled(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, successBody("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.CompleteResult(ctx, []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("server calls = %d, want 0", calls)
	}
}

func TestParentDeadline(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.CompleteResult(ctx, []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.TimeoutSeconds = 1
	client, _ := newTestClient(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	})

	start := time.Now()
	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("timeout after %v, want about 1s", elapsed)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v1/chat/completions" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		writeJSON(w, http.StatusOK, successBody("should not be reached"))
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("CompleteResult error = nil, want redirect error")
	}
	var httpErr *llm.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want HTTP 302", err)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1", calls)
	}
}

func TestNoRetry(t *testing.T) {
	calls := 0
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"message": "server error"},
		})
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("CompleteResult error = nil, want error")
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry)", calls)
	}
}

func TestMessageValidation(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
	}{
		{"empty", nil},
		{"invalid role", []llm.Message{{Role: "tool", Content: "x"}}},
		{"empty content", []llm.Message{{Role: "user", Content: "  "}}},
	}

	calls := 0
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, successBody("ok"))
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.CompleteResult(context.Background(), tt.messages)
			if !errors.Is(err, llm.ErrInvalidMessages) {
				t.Fatalf("error = %v, want ErrInvalidMessages", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("server calls = %d, want 0", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWithHTTPClientAndRequestError(t *testing.T) {
	called := false
	hc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("network down")
	})}
	client, err := llm.New(testConfig(), llm.WithHTTPClient(hc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrRequest) {
		t.Fatalf("error = %v, want ErrRequest", err)
	}
	if !called {
		t.Fatal("injected HTTP client was not used")
	}
}

func TestCompleteError(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "boom"}})
	})

	if _, err := client.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("Complete error = nil, want error")
	}
}

func TestHTTPErrorString(t *testing.T) {
	if got := (&llm.HTTPError{StatusCode: 500, Status: "500 Internal Server Error"}).Error(); got != "llm: http 500 Internal Server Error" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&llm.HTTPError{StatusCode: 500, Status: "500 Internal Server Error", Message: "boom"}).Error(); got != "llm: http 500 Internal Server Error: boom" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&llm.HTTPError{StatusCode: 500}).Error(); got != "llm: http 500" {
		t.Fatalf("Error() = %q", got)
	}
	var nilErr *llm.HTTPError
	if got := nilErr.Error(); got != "llm: http error" {
		t.Fatalf("nil Error() = %q", got)
	}
}

func TestResponseTooLarge(t *testing.T) {
	client, _ := newTestClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(4<<20)+1))
	})

	_, err := client.CompleteResult(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, llm.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}
