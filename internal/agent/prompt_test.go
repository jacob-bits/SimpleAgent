package agent

import (
	"strings"
	"testing"

	"simpleagent/internal/tool"
)

func namedDefinition(name, description string) tool.ToolDefinition {
	def := testDefinition()
	def.Metadata.Name = name
	def.Metadata.Description = description
	return def
}

func TestBuildSystemPromptProjectsOnlyPublicToolFields(t *testing.T) {
	def := testDefinition()
	rate := 99
	def.Metadata.Version = "9.9.9"
	def.Metadata.RateLimit = &rate
	def.Metadata.TimeoutSeconds = 77
	def.Metadata.MemoryLimitMB = 88
	def.Metadata.Sandboxed = true
	def.Metadata.Dangerous = true
	def.Metadata.RequiresAuth = true
	def.Metadata.CostPerUse = 123

	prompt, err := buildSystemPrompt([]tool.ToolDefinition{def})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}

	for _, want := range []string{
		"### read_file",
		"Read a text file",
		"Input schema:",
		"\"path\"",
		"\"type\": \"object\"",
		"status=\"ok\"",
		"请求澄清",
		"必须用 final_answer",
		"output",
		"error",
		"<control>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}

	for _, forbidden := range []string{
		"rate_limit", "timeout_seconds", "memory_limit_mb", "sandboxed",
		"dangerous", "requires_auth", "cost_per_use", "9.9.9",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt leaked policy field %q: %s", forbidden, prompt)
		}
	}
}

func TestBuildSystemPromptToolOrder(t *testing.T) {
	first := namedDefinition("first_tool", "First")
	second := namedDefinition("second_tool", "Second")
	prompt, err := buildSystemPrompt([]tool.ToolDefinition{first, second})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	if strings.Index(prompt, "### first_tool") > strings.Index(prompt, "### second_tool") {
		t.Fatalf("tool order changed: %s", prompt)
	}
}

func TestBuildSystemPromptZeroTools(t *testing.T) {
	prompt, err := buildSystemPrompt(nil)
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	if !strings.Contains(prompt, "（无可用工具）") {
		t.Fatalf("prompt missing empty tool list: %s", prompt)
	}
	if strings.Contains(prompt, "### ") {
		t.Fatalf("prompt contains a tool heading without tools: %s", prompt)
	}
}

func TestBuildSystemPromptDeterministic(t *testing.T) {
	first, err := buildSystemPrompt([]tool.ToolDefinition{testDefinition()})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	second, err := buildSystemPrompt([]tool.ToolDefinition{testDefinition()})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	if first != second {
		t.Fatal("buildSystemPrompt is not deterministic")
	}
}

func TestBuildSystemPromptSchemaError(t *testing.T) {
	def := testDefinition()
	def.Parameters[0].Default = func() {}
	if _, err := buildSystemPrompt([]tool.ToolDefinition{def}); err == nil {
		t.Fatal("buildSystemPrompt() error = nil, want schema error")
	}
}
