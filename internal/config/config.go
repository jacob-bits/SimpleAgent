// Package config defines SimpleAgent's runtime configuration: built-in
// defaults, JSON file overrides, API key resolution, and validation.
//
// The precedence implemented here matches DESIGN.md §7.3:
//
//	built-in defaults < JSON config file < explicit command-line flags
//
// Command-line overrides are intentionally left to the CLI layer; this
// package only provides the defaults and the JSON file layer on top of them.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// ErrInvalidConfig marks every configuration failure (unreadable file,
// malformed JSON, unknown fields, or failed validation). The CLI maps it to
// exit code 2.
var ErrInvalidConfig = errors.New("invalid configuration")

// Config is the root configuration object loaded from JSON.
type Config struct {
	LLM   LLMConfig   `json:"llm"`
	Agent AgentConfig `json:"agent"`
	Tools ToolsConfig `json:"tools"`
}

// LLMConfig configures the OpenAI-compatible Chat Completions adapter.
type LLMConfig struct {
	BaseURL         string  `json:"base_url"`
	APIKey          string  `json:"api_key"`
	APIKeyEnv       string  `json:"api_key_env"`
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature"`
	MaxTokens       int     `json:"max_tokens"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
	ReasoningEffort string  `json:"reasoning_effort"`
}

// AgentConfig configures the ReAct loop and its termination guards.
type AgentConfig struct {
	MinSteps            int     `json:"min_steps"`
	MaxSteps            int     `json:"max_steps"`
	MaxRepeatedActions  int     `json:"max_repeated_actions"`
	NoProgressThreshold float64 `json:"no_progress_threshold"`
	NoProgressMinChars  int     `json:"no_progress_min_chars"`
	MaxHistoryChars     int     `json:"max_history_chars"` // UTF-8 bytes; name kept for compatibility.
	TimeoutSeconds      int     `json:"timeout_seconds"`
	Verbose             bool    `json:"verbose"`
}

// ToolsConfig configures the read-only file tools.
type ToolsConfig struct {
	AllowedRoot  string `json:"allowed_root"`
	MaxFileBytes int64  `json:"max_file_bytes"`
}

// Default returns the built-in configuration described in DESIGN.md §7.2.
// The returned value is fully populated and passes Validate.
func Default() *Config {
	return &Config{
		LLM: LLMConfig{
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "",
			APIKeyEnv:      "OPENAI_API_KEY",
			Model:          "gpt-4o-mini",
			Temperature:    0,
			MaxTokens:      1024,
			TimeoutSeconds: 60,
		},
		Agent: AgentConfig{
			MinSteps:            1,
			MaxSteps:            8,
			MaxRepeatedActions:  2,
			NoProgressThreshold: 0.95,
			NoProgressMinChars:  80,
			MaxHistoryChars:     200000,
			TimeoutSeconds:      120,
			Verbose:             false,
		},
		Tools: ToolsConfig{
			AllowedRoot:  ".",
			MaxFileBytes: 65536,
		},
	}
}

// Load reads a JSON config file over the built-in defaults and validates the
// result. An empty path means "no config file": the defaults are returned.
//
// Decoding is strict: unknown fields, a non-object top level, and trailing
// JSON values are rejected rather than silently ignored. Fields omitted from
// the file keep their default value; fields explicitly set to a zero value
// (for example "no_progress_threshold": 0) override the default as expected.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %w", ErrInvalidConfig, path, err)
		}
		if err := decode(data, cfg); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalidConfig, path, err)
		}
	}

	if err := cfg.Validate(); err != nil {
		if path == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// decode overlays one JSON object onto cfg. It rejects anything that is not a
// single top-level object so that a typo cannot silently degrade to defaults.
func decode(data []byte, cfg *Config) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return errors.New("file is empty")
	}
	if trimmed[0] != '{' {
		return errors.New("top-level value must be a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}

	var trailing json.RawMessage
	switch err := dec.Decode(&trailing); {
	case err == nil:
		return errors.New("unexpected data after top-level JSON object")
	case !errors.Is(err, io.EOF):
		return fmt.Errorf("unexpected trailing data: %w", err)
	}
	return nil
}

// Validate checks every documented constraint in DESIGN.md §7.2 and reports
// all violations at once. The returned error matches ErrInvalidConfig.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !validHTTPURL(c.LLM.BaseURL) {
		add("llm.base_url must be an absolute http(s) URL, got %q", c.LLM.BaseURL)
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		add("llm.model is required")
	}
	if t := c.LLM.Temperature; !(t >= 0 && t <= 2) {
		add("llm.temperature must be within [0, 2], got %v", c.LLM.Temperature)
	}
	if c.LLM.MaxTokens < 0 {
		add("llm.max_tokens must be >= 0, got %d", c.LLM.MaxTokens)
	}
	if c.LLM.TimeoutSeconds <= 0 {
		add("llm.timeout_seconds must be > 0, got %d", c.LLM.TimeoutSeconds)
	}
	if !validReasoningEffort(c.LLM.ReasoningEffort) {
		add("llm.reasoning_effort is invalid: %q", c.LLM.ReasoningEffort)
	}

	if c.Agent.MinSteps < 1 {
		add("agent.min_steps must be >= 1, got %d", c.Agent.MinSteps)
	}
	if c.Agent.MaxSteps < 1 {
		add("agent.max_steps must be >= 1, got %d", c.Agent.MaxSteps)
	}
	if c.Agent.MinSteps >= 1 && c.Agent.MaxSteps >= 1 && c.Agent.MinSteps > c.Agent.MaxSteps {
		add("agent.min_steps (%d) must be <= agent.max_steps (%d)", c.Agent.MinSteps, c.Agent.MaxSteps)
	}
	if c.Agent.MaxRepeatedActions < 1 {
		add("agent.max_repeated_actions must be >= 1, got %d", c.Agent.MaxRepeatedActions)
	}
	if t := c.Agent.NoProgressThreshold; !(t >= 0 && t <= 1) {
		add("agent.no_progress_threshold must be within [0, 1], got %v", c.Agent.NoProgressThreshold)
	}
	if c.Agent.NoProgressMinChars < 0 {
		add("agent.no_progress_min_chars must be >= 0, got %d", c.Agent.NoProgressMinChars)
	}
	if c.Agent.MaxHistoryChars <= 0 {
		add("agent.max_history_chars must be > 0, got %d", c.Agent.MaxHistoryChars)
	}
	if c.Agent.TimeoutSeconds <= 0 {
		add("agent.timeout_seconds must be > 0, got %d", c.Agent.TimeoutSeconds)
	}

	if strings.TrimSpace(c.Tools.AllowedRoot) == "" {
		add("tools.allowed_root is required")
	}
	if c.Tools.MaxFileBytes <= 0 {
		add("tools.max_file_bytes must be > 0, got %d", c.Tools.MaxFileBytes)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
}

// validHTTPURL reports whether raw is an absolute http/https URL with a host.
func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}

// validReasoningEffort accepts the DeepSeek/OpenAI reasoning effort values we
// know about. An empty value means "do not send the field".
func validReasoningEffort(v string) bool {
	switch v {
	case "", "none", "low", "high", "max", "minimal", "medium", "xhigh":
		return true
	default:
		return false
	}
}

// ResolveAPIKey returns the effective API key using the process environment.
// An explicitly configured llm.api_key always wins over llm.api_key_env.
// The key is resolved on demand and is never written back into Config, which
// keeps accidental logging of the struct from leaking it.
func (c *Config) ResolveAPIKey() string {
	return c.LLM.ResolveAPIKey()
}

// ResolveAPIKeyWithEnv is ResolveAPIKey with an injectable environment lookup,
// so tests do not need to mutate the real process environment.
func (c *Config) ResolveAPIKeyWithEnv(getenv func(string) string) string {
	return c.LLM.ResolveAPIKeyWithEnv(getenv)
}

// ResolveAPIKey returns the effective API key using the process environment.
func (l LLMConfig) ResolveAPIKey() string {
	return l.ResolveAPIKeyWithEnv(os.Getenv)
}

// ResolveAPIKeyWithEnv is LLMConfig.ResolveAPIKey with an injectable lookup.
func (l LLMConfig) ResolveAPIKeyWithEnv(getenv func(string) string) string {
	if l.APIKey != "" {
		return l.APIKey
	}
	if l.APIKeyEnv == "" || getenv == nil {
		return ""
	}
	return getenv(l.APIKeyEnv)
}
