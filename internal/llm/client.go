package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"simpleagent/internal/config"
)

const (
	maxResponseBytes     int64 = 4 << 20
	maxErrorMessageRunes       = 512
)

// Client is a concurrency-safe OpenAI-compatible Chat Completions client.
type Client struct {
	endpoint        string
	apiKey          string
	model           string
	temperature     float64
	maxTokens       int
	reasoningEffort string
	timeout         time.Duration
	http            *http.Client
}

// Compile-time check: Client implements the Agent Completer contract.
var _ interface {
	Complete(context.Context, []Message) (string, error)
} = (*Client)(nil)

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client. The injected client is
// responsible for its own redirect and TLS policy.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.http = client
		}
	}
}

// New validates the configuration and constructs a Client. It does not make
// any network request.
func New(cfg config.LLMConfig, opts ...Option) (*Client, error) {
	endpoint, err := buildEndpoint(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("llm: model is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("llm: timeout_seconds must be > 0, got %d", cfg.TimeoutSeconds)
	}
	if t := cfg.Temperature; !(t >= 0 && t <= 2) {
		return nil, fmt.Errorf("llm: temperature must be within [0, 2], got %v", cfg.Temperature)
	}
	if cfg.MaxTokens < 0 {
		return nil, fmt.Errorf("llm: max_tokens must be >= 0, got %d", cfg.MaxTokens)
	}
	if !validReasoningEffort(cfg.ReasoningEffort) {
		return nil, fmt.Errorf("llm: reasoning_effort is invalid: %q", cfg.ReasoningEffort)
	}

	c := &Client{
		endpoint:        endpoint,
		apiKey:          cfg.ResolveAPIKey(),
		model:           cfg.Model,
		temperature:     cfg.Temperature,
		maxTokens:       cfg.MaxTokens,
		reasoningEffort: cfg.ReasoningEffort,
		timeout:         time.Duration(cfg.TimeoutSeconds) * time.Second,
		http:            defaultHTTPClient(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.http == nil {
		c.http = defaultHTTPClient()
	}
	return c, nil
}

// Complete performs one completion and returns only the assistant content.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := c.CompleteResult(ctx, messages)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CompleteResult performs one completion and returns content, reasoning,
// finish reason and token usage.
func (c *Client) CompleteResult(ctx context.Context, messages []Message) (Result, error) {
	if c == nil || c.http == nil || c.endpoint == "" {
		return Result{}, errors.New("llm: client is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validateMessages(messages); err != nil {
		return Result{}, err
	}

	payload := chatRequest{
		Model:           c.model,
		Messages:        messages,
		Temperature:     c.temperature,
		MaxTokens:       c.maxTokens,
		ReasoningEffort: c.reasoningEffort,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("%w: encode request: %v", ErrRequest, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("%w: build request: %v", ErrRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, classifyRequestError(ctx, reqCtx, err)
	}
	defer resp.Body.Close()

	data, tooLarge, err := readCapped(resp.Body, maxResponseBytes)
	if err != nil {
		return Result{}, classifyRequestError(ctx, reqCtx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, parseHTTPError(resp, data)
	}
	if tooLarge {
		return Result{}, ErrResponseTooLarge
	}

	var response chatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	if len(response.Choices) == 0 {
		return Result{}, ErrEmptyChoices
	}

	choice := response.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" {
		return Result{}, fmt.Errorf("%w (finish_reason=%s)", ErrEmptyContent, choice.FinishReason)
	}

	var usage Usage
	if response.Usage != nil {
		usage = *response.Usage
	}
	model := response.Model
	if model == "" {
		model = c.model
	}
	return Result{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ID:               response.ID,
		Model:            model,
		FinishReason:     choice.FinishReason,
		Usage:            usage,
	}, nil
}

func classifyRequestError(ctx, reqCtx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if reqCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return fmt.Errorf("%w: %v", ErrRequest, err)
}

func validateMessages(messages []Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("%w: messages are empty", ErrInvalidMessages)
	}
	for i, message := range messages {
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("%w: message[%d] has invalid role %q", ErrInvalidMessages, i, message.Role)
		}
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Errorf("%w: message[%d] content is empty", ErrInvalidMessages, i)
		}
	}
	return nil
}

func buildEndpoint(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return "", errors.New("llm: base_url is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("llm: invalid base_url: %v", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("llm: base_url must be http(s), got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("llm: base_url must include a host")
	}
	if u.User != nil {
		return "", errors.New("llm: base_url must not include userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("llm: base_url must not include query or fragment")
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/chat/completions") {
		return "", errors.New("llm: base_url must not include /chat/completions")
	}
	u.Path = path + "/chat/completions"
	u.RawPath = ""
	return u.String(), nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "low", "high", "max", "minimal", "medium", "xhigh":
		return true
	default:
		return false
	}
}

func readCapped(r io.Reader, maxBytes int64) ([]byte, bool, error) {
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func parseHTTPError(resp *http.Response, body []byte) *HTTPError {
	var envelope apiErrorEnvelope
	_ = json.Unmarshal(body, &envelope)
	message := truncateRunes(strings.TrimSpace(envelope.Error.Message), maxErrorMessageRunes)
	return &HTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    message,
	}
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Temperature     float64   `json:"temperature"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
}

type responseMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatChoice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *Usage       `json:"usage,omitempty"`
}

type apiErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}
