package config_test

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"simpleagent/internal/config"
)

// writeConfig stores body in a fresh temporary file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestDefault(t *testing.T) {
	want := &config.Config{
		LLM: config.LLMConfig{
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "",
			APIKeyEnv:      "OPENAI_API_KEY",
			Model:          "gpt-4o-mini",
			Temperature:    0,
			MaxTokens:      1024,
			TimeoutSeconds: 60,
		},
		Agent: config.AgentConfig{
			MinSteps:            1,
			MaxSteps:            8,
			MaxRepeatedActions:  2,
			NoProgressThreshold: 0.95,
			NoProgressMinChars:  80,
			MaxHistoryChars:     200000,
			TimeoutSeconds:      120,
			Verbose:             false,
		},
		Tools: config.ToolsConfig{
			AllowedRoot:  ".",
			MaxFileBytes: 65536,
		},
	}

	got := config.Default()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Default() = %#v, want %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("default config should pass validation: %v", err)
	}
}

func TestLoadWithoutPathUsesDefaults(t *testing.T) {
	got, err := config.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v", err)
	}
	if !reflect.DeepEqual(got, config.Default()) {
		t.Fatalf("Load(\"\") = %#v, want defaults", got)
	}
}

func TestLoadPartialOverride(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `{
		"llm": {
			"base_url": "https://api.deepseek.com",
			"api_key": "sk-explicit",
			"model": "deepseek-flash",
			"reasoning_effort": "none"
		},
		"agent": {
			"min_steps": 2,
			"max_steps": 5,
			"no_progress_threshold": 0,
			"verbose": true
		},
		"tools": {
			"allowed_root": "/tmp/root"
		}
	}`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}

	// Overridden values.
	check("llm.base_url", cfg.LLM.BaseURL, "https://api.deepseek.com")
	check("llm.api_key", cfg.LLM.APIKey, "sk-explicit")
	check("llm.model", cfg.LLM.Model, "deepseek-flash")
	check("llm.reasoning_effort", cfg.LLM.ReasoningEffort, "none")
	check("agent.min_steps", cfg.Agent.MinSteps, 2)
	check("agent.max_steps", cfg.Agent.MaxSteps, 5)
	check("agent.verbose", cfg.Agent.Verbose, true)
	check("tools.allowed_root", cfg.Tools.AllowedRoot, "/tmp/root")

	// Omitted values keep their defaults.
	check("llm.api_key_env", cfg.LLM.APIKeyEnv, "OPENAI_API_KEY")
	check("llm.temperature", cfg.LLM.Temperature, float64(0))
	check("llm.max_tokens", cfg.LLM.MaxTokens, 1024)
	check("llm.timeout_seconds", cfg.LLM.TimeoutSeconds, 60)
	check("agent.max_repeated_actions", cfg.Agent.MaxRepeatedActions, 2)
	check("agent.no_progress_min_chars", cfg.Agent.NoProgressMinChars, 80)
	check("agent.max_history_chars", cfg.Agent.MaxHistoryChars, 200000)
	check("agent.timeout_seconds", cfg.Agent.TimeoutSeconds, 120)
	check("tools.max_file_bytes", cfg.Tools.MaxFileBytes, int64(65536))

	// An explicit zero must override a non-zero default (threshold 0 disables
	// the no-progress guard).
	check("agent.no_progress_threshold", cfg.Agent.NoProgressThreshold, float64(0))
}

func TestReasoningEffortValidation(t *testing.T) {
	for _, value := range []string{"", "none", "low", "high", "max", "minimal", "medium", "xhigh"} {
		cfg := config.Default()
		cfg.LLM.ReasoningEffort = value
		if err := cfg.Validate(); err != nil {
			t.Errorf("ReasoningEffort %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"extreme", "NONE", " medium"} {
		cfg := config.Default()
		cfg.LLM.ReasoningEffort = value
		if err := cfg.Validate(); err == nil {
			t.Errorf("ReasoningEffort %q accepted, want error", value)
		}
	}
}

func TestLoadOmittedSectionKeepsDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, `{"agent":{"verbose":true}}`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defaults := config.Default()
	if !reflect.DeepEqual(cfg.LLM, defaults.LLM) {
		t.Errorf("llm = %#v, want defaults %#v", cfg.LLM, defaults.LLM)
	}
	if !reflect.DeepEqual(cfg.Tools, defaults.Tools) {
		t.Errorf("tools = %#v, want defaults %#v", cfg.Tools, defaults.Tools)
	}
	if !cfg.Agent.Verbose {
		t.Error("agent.verbose = false, want true")
	}
}

func TestLoadExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", path, err)
	}
	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Fatalf("config.example.json = %#v, want defaults %#v", cfg, config.Default())
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty file", "", "file is empty"},
		{"top-level null", "null", "top-level value must be a JSON object"},
		{"top-level array", "[]", "top-level value must be a JSON object"},
		{"malformed json", `{"llm":`, "decode JSON"},
		{"unknown top-level field", `{"nope": true}`, "unknown field"},
		{"unknown nested field", `{"agent":{"nope": true}}`, "unknown field"},
		{"trailing json value", `{"agent":{"min_steps":2}} {"agent":{"min_steps":3}}`, "after top-level JSON object"},
		{"invalid value", `{"agent":{"min_steps":0}}`, "agent.min_steps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Errorf("errors.Is(err, ErrInvalidConfig) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Errorf("errors.Is(err, ErrInvalidConfig) = false; err = %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false; err = %v", err)
	}
}

func TestValidateAcceptsBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"temperature upper bound", func(c *config.Config) { c.LLM.Temperature = 2 }},
		{"max tokens zero means server default", func(c *config.Config) { c.LLM.MaxTokens = 0 }},
		{"no progress disabled with zero", func(c *config.Config) { c.Agent.NoProgressThreshold = 0 }},
		{"no progress threshold one", func(c *config.Config) { c.Agent.NoProgressThreshold = 1 }},
		{"min steps equals max steps", func(c *config.Config) { c.Agent.MinSteps = 1; c.Agent.MaxSteps = 1 }},
		{"max repeated actions one", func(c *config.Config) { c.Agent.MaxRepeatedActions = 1 }},
		{"no progress min chars zero", func(c *config.Config) { c.Agent.NoProgressMinChars = 0 }},
		{"http base url", func(c *config.Config) { c.LLM.BaseURL = "http://localhost:8080/v1" }},
		{"filesystem root", func(c *config.Config) { c.Tools.AllowedRoot = "/" }},
		{"max file bytes one", func(c *config.Config) { c.Tools.MaxFileBytes = 1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   []string
	}{
		{"empty base url", func(c *config.Config) { c.LLM.BaseURL = "" }, []string{"llm.base_url"}},
		{"relative base url", func(c *config.Config) { c.LLM.BaseURL = "api.openai.com/v1" }, []string{"llm.base_url"}},
		{"unsupported scheme", func(c *config.Config) { c.LLM.BaseURL = "ftp://example.com/v1" }, []string{"llm.base_url"}},
		{"missing host", func(c *config.Config) { c.LLM.BaseURL = "https://" }, []string{"llm.base_url"}},
		{"empty model", func(c *config.Config) { c.LLM.Model = "" }, []string{"llm.model"}},
		{"blank model", func(c *config.Config) { c.LLM.Model = "   " }, []string{"llm.model"}},
		{"negative temperature", func(c *config.Config) { c.LLM.Temperature = -0.01 }, []string{"llm.temperature"}},
		{"temperature too high", func(c *config.Config) { c.LLM.Temperature = 2.01 }, []string{"llm.temperature"}},
		{"temperature NaN", func(c *config.Config) { c.LLM.Temperature = math.NaN() }, []string{"llm.temperature"}},
		{"negative max tokens", func(c *config.Config) { c.LLM.MaxTokens = -1 }, []string{"llm.max_tokens"}},
		{"zero llm timeout", func(c *config.Config) { c.LLM.TimeoutSeconds = 0 }, []string{"llm.timeout_seconds"}},
		{"invalid reasoning effort", func(c *config.Config) { c.LLM.ReasoningEffort = "extreme" }, []string{"llm.reasoning_effort"}},
		{"min steps zero", func(c *config.Config) { c.Agent.MinSteps = 0 }, []string{"agent.min_steps"}},
		{"max steps zero", func(c *config.Config) { c.Agent.MaxSteps = 0 }, []string{"agent.max_steps"}},
		{"min steps above max", func(c *config.Config) { c.Agent.MinSteps = 5; c.Agent.MaxSteps = 4 }, []string{"agent.min_steps", "agent.max_steps"}},
		{"max repeated actions zero", func(c *config.Config) { c.Agent.MaxRepeatedActions = 0 }, []string{"agent.max_repeated_actions"}},
		{"negative no progress threshold", func(c *config.Config) { c.Agent.NoProgressThreshold = -0.01 }, []string{"agent.no_progress_threshold"}},
		{"no progress threshold too high", func(c *config.Config) { c.Agent.NoProgressThreshold = 1.01 }, []string{"agent.no_progress_threshold"}},
		{"no progress threshold NaN", func(c *config.Config) { c.Agent.NoProgressThreshold = math.NaN() }, []string{"agent.no_progress_threshold"}},
		{"negative no progress min chars", func(c *config.Config) { c.Agent.NoProgressMinChars = -1 }, []string{"agent.no_progress_min_chars"}},
		{"zero history budget", func(c *config.Config) { c.Agent.MaxHistoryChars = 0 }, []string{"agent.max_history_chars"}},
		{"zero agent timeout", func(c *config.Config) { c.Agent.TimeoutSeconds = 0 }, []string{"agent.timeout_seconds"}},
		{"blank allowed root", func(c *config.Config) { c.Tools.AllowedRoot = "   " }, []string{"tools.allowed_root"}},
		{"zero max file bytes", func(c *config.Config) { c.Tools.MaxFileBytes = 0 }, []string{"tools.max_file_bytes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Errorf("errors.Is(err, ErrInvalidConfig) = false; err = %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Model = ""
	cfg.Agent.MaxSteps = 0
	cfg.Tools.MaxFileBytes = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
	for _, want := range []string{"llm.model", "agent.max_steps", "tools.max_file_bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Run("explicit key wins", func(t *testing.T) {
		cfg := config.Default()
		cfg.LLM.APIKey = "sk-explicit"
		cfg.LLM.APIKeyEnv = "SHOULD_NOT_BE_READ"

		got := cfg.ResolveAPIKeyWithEnv(func(name string) string {
			t.Fatalf("environment lookup called for %q despite explicit api_key", name)
			return ""
		})
		if got != "sk-explicit" {
			t.Errorf("ResolveAPIKeyWithEnv() = %q, want %q", got, "sk-explicit")
		}
	})

	t.Run("environment fallback", func(t *testing.T) {
		cfg := config.Default()
		cfg.LLM.APIKeyEnv = "TEST_PROVIDER_KEY"

		got := cfg.ResolveAPIKeyWithEnv(func(name string) string {
			if name != "TEST_PROVIDER_KEY" {
				t.Fatalf("lookup name = %q, want %q", name, "TEST_PROVIDER_KEY")
			}
			return "sk-from-env"
		})
		if got != "sk-from-env" {
			t.Errorf("ResolveAPIKeyWithEnv() = %q, want %q", got, "sk-from-env")
		}
	})

	t.Run("missing environment variable", func(t *testing.T) {
		cfg := config.Default()
		cfg.LLM.APIKeyEnv = "MISSING_KEY"
		if got := cfg.ResolveAPIKeyWithEnv(func(string) string { return "" }); got != "" {
			t.Errorf("ResolveAPIKeyWithEnv() = %q, want empty", got)
		}
	})

	t.Run("empty env name", func(t *testing.T) {
		cfg := config.Default()
		cfg.LLM.APIKeyEnv = ""

		got := cfg.ResolveAPIKeyWithEnv(func(name string) string {
			t.Fatalf("environment lookup called for empty env name %q", name)
			return ""
		})
		if got != "" {
			t.Errorf("ResolveAPIKeyWithEnv() = %q, want empty", got)
		}
	})

	t.Run("reads process environment", func(t *testing.T) {
		t.Setenv("SIMPLEAGENT_TEST_API_KEY", "sk-from-process")
		cfg := config.Default()
		cfg.LLM.APIKeyEnv = "SIMPLEAGENT_TEST_API_KEY"

		if got := cfg.ResolveAPIKey(); got != "sk-from-process" {
			t.Errorf("ResolveAPIKey() = %q, want %q", got, "sk-from-process")
		}
	})
}
