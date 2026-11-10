// Package llm implements an OpenAI-compatible Chat Completions adapter.
package llm

import (
	"errors"
	"fmt"
)

// Message is one message in a chat completion request.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionTokensDetails is the optional breakdown of generated tokens.
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// Usage is the token accounting returned by the provider.
type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptCacheHitTokens    int                      `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   int                      `json:"prompt_cache_miss_tokens,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// Result is the full result of one successful completion.
// Complete returns only Content; the remaining fields are for tracing,
// finish-reason handling and token budgets.
type Result struct {
	Content          string
	ReasoningContent string
	ID               string
	Model            string
	FinishReason     string
	Usage            Usage
}

// HTTPError represents a non-2xx response. Message is limited to the
// provider's sanitized error.message and never includes request contents or
// the Authorization header.
type HTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

// Error implements error.
func (e *HTTPError) Error() string {
	if e == nil {
		return "llm: http error"
	}
	status := e.Status
	if status == "" {
		status = fmt.Sprintf("%d", e.StatusCode)
	}
	if e.Message != "" {
		return fmt.Sprintf("llm: http %s: %s", status, e.Message)
	}
	return fmt.Sprintf("llm: http %s", status)
}

var (
	ErrRequest          = errors.New("llm: request failed")
	ErrDecode           = errors.New("llm: decode response failed")
	ErrEmptyChoices     = errors.New("llm: response contains no choices")
	ErrEmptyContent     = errors.New("llm: response content is empty")
	ErrResponseTooLarge = errors.New("llm: response too large")
	ErrTimeout          = errors.New("llm: request timed out")
	ErrInvalidMessages  = errors.New("llm: invalid messages")
)
