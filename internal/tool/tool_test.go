package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"simpleagent/internal/tool"
)

func TestMetadataDefaults(t *testing.T) {
	got := tool.DefaultToolMetadata()
	want := tool.ToolMetadata{
		TimeoutSeconds: 30,
		MemoryLimitMB:  512,
		Sandboxed:      true,
		CostPerUse:     0,
	}
	if got.TimeoutSeconds != want.TimeoutSeconds ||
		got.MemoryLimitMB != want.MemoryLimitMB ||
		got.Sandboxed != want.Sandboxed ||
		got.CostPerUse != want.CostPerUse {
		t.Fatalf("DefaultToolMetadata() = %+v, want %+v", got, want)
	}
	if got.RateLimit != nil {
		t.Fatalf("RateLimit = %v, want nil", got.RateLimit)
	}

	meta := tool.NewToolMetadata("demo", "1.0.0", "demo tool", tool.CategoryFile)
	if meta.Name != "demo" || meta.Version != "1.0.0" || meta.Category != tool.CategoryFile {
		t.Fatalf("NewToolMetadata did not set identity: %+v", meta)
	}
	if meta.TimeoutSeconds != 30 || meta.MemoryLimitMB != 512 || !meta.Sandboxed {
		t.Fatalf("NewToolMetadata did not apply defaults: %+v", meta)
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
}

func TestMetadataJSONDefaults(t *testing.T) {
	var meta tool.ToolMetadata
	body := `{"name":"demo","version":"1.0.0","description":"demo","category":"file"}`
	if err := json.Unmarshal([]byte(body), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.TimeoutSeconds != 30 || meta.MemoryLimitMB != 512 || !meta.Sandboxed {
		t.Fatalf("defaults not applied from JSON: %+v", meta)
	}
	if meta.RateLimit != nil {
		t.Fatalf("rate_limit = %v, want nil", meta.RateLimit)
	}

	if err := json.Unmarshal([]byte(`{"name":"demo","version":"1","description":"d","category":"file","sandboxed":false,"rate_limit":60}`), &meta); err != nil {
		t.Fatalf("unmarshal explicit values: %v", err)
	}
	if meta.Sandboxed {
		t.Fatal("sandboxed = true, want explicit false")
	}
	if meta.RateLimit == nil || *meta.RateLimit != 60 {
		t.Fatalf("rate_limit = %v, want 60", meta.RateLimit)
	}
}

func TestMetadataValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tool.ToolMetadata)
		want   string
	}{
		{"empty name", func(m *tool.ToolMetadata) { m.Name = "" }, "name must match"},
		{"invalid name", func(m *tool.ToolMetadata) { m.Name = "Bad-Name" }, "name must match"},
		{"empty version", func(m *tool.ToolMetadata) { m.Version = "" }, "version is required"},
		{"empty description", func(m *tool.ToolMetadata) { m.Description = "   " }, "description is required"},
		{"unknown category", func(m *tool.ToolMetadata) { m.Category = "other" }, "category"},
		{"zero rate limit", func(m *tool.ToolMetadata) { m.RateLimit = tool.Ptr(0) }, "rate_limit"},
		{"negative rate limit", func(m *tool.ToolMetadata) { m.RateLimit = tool.Ptr(-1) }, "rate_limit"},
		{"zero timeout", func(m *tool.ToolMetadata) { m.TimeoutSeconds = 0 }, "timeout_seconds"},
		{"zero memory", func(m *tool.ToolMetadata) { m.MemoryLimitMB = 0 }, "memory_limit_mb"},
		{"negative cost", func(m *tool.ToolMetadata) { m.CostPerUse = -1 }, "cost_per_use"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := tool.NewToolMetadata("demo", "1.0.0", "demo", tool.CategoryFile)
			tt.mutate(&meta)
			err := meta.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParameterDefaults(t *testing.T) {
	p := tool.NewToolParameter("path", tool.ParameterString, "path")
	if !p.Required {
		t.Fatal("NewToolParameter Required = false, want true")
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid parameter rejected: %v", err)
	}

	var fromJSON tool.ToolParameter
	if err := json.Unmarshal([]byte(`{"name":"path","type":"string","description":"path"}`), &fromJSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !fromJSON.Required {
		t.Fatal("required default = false, want true")
	}

	if err := json.Unmarshal([]byte(`{"name":"path","type":"string","description":"path","required":false}`), &fromJSON); err != nil {
		t.Fatalf("unmarshal explicit optional: %v", err)
	}
	if fromJSON.Required {
		t.Fatal("required = true, want explicit false")
	}
}

func TestParameterValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tool.ToolParameter)
		want   string
	}{
		{"invalid name", func(p *tool.ToolParameter) { p.Name = "bad-name" }, "name must match"},
		{"invalid type", func(p *tool.ToolParameter) { p.Type = "date" }, "type"},
		{"empty description", func(p *tool.ToolParameter) { p.Description = "" }, "description is required"},
		{"required with default", func(p *tool.ToolParameter) { p.Default = "x" }, "default is only allowed"},
		{"default type mismatch", func(p *tool.ToolParameter) { p.Required = false; p.Default = 123 }, "default:"},
		{"invalid enum member", func(p *tool.ToolParameter) { p.Required = false; p.Enum = []any{"a", 1} }, "enum[1]"},
		{"default outside enum", func(p *tool.ToolParameter) {
			p.Required = false
			p.Enum = []any{"a"}
			p.Default = "b"
		}, "default:"},
		{"min on string", func(p *tool.ToolParameter) { p.MinValue = tool.Ptr(1.0) }, "only allowed for numeric"},
		{"min above max", func(p *tool.ToolParameter) {
			p.Type = tool.ParameterInteger
			p.Required = false
			p.MinValue = tool.Ptr(10.0)
			p.MaxValue = tool.Ptr(1.0)
		}, "must be <= max_value"},
		{"non-integral integer min", func(p *tool.ToolParameter) {
			p.Type = tool.ParameterInteger
			p.MinValue = tool.Ptr(1.5)
		}, "must be an integer"},
		{"invalid pattern", func(p *tool.ToolParameter) { p.Pattern = tool.Ptr("[") }, "not a valid regular expression"},
		{"pattern on integer", func(p *tool.ToolParameter) {
			p.Type = tool.ParameterInteger
			p.Pattern = tool.Ptr("^[0-9]+$")
		}, "pattern is only allowed"},
		{"default violates pattern", func(p *tool.ToolParameter) {
			p.Required = false
			p.Pattern = tool.Ptr("^[a-z]+$")
			p.Default = "ABC"
		}, "default:"},
		{"non-serializable default", func(p *tool.ToolParameter) {
			p.Required = false
			p.Default = func() {}
		}, "not JSON-serializable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tool.NewToolParameter("path", tool.ParameterString, "path")
			tt.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDefinitionInputSchema(t *testing.T) {
	def := tool.ToolDefinition{Metadata: baseMetadata("demo")}
	def.Parameters = []tool.ToolParameter{
		tool.NewToolParameter("path", tool.ParameterString, "path to read"),
	}
	limit := tool.NewToolParameter("limit", tool.ParameterInteger, "limit")
	limit.Required = false
	limit.Default = 10
	limit.MinValue = tool.Ptr(1.0)
	limit.MaxValue = tool.Ptr(100.0)
	def.Parameters = append(def.Parameters, limit)

	mode := tool.NewToolParameter("mode", tool.ParameterString, "mode")
	mode.Required = false
	mode.Enum = []any{"fast", "slow"}
	mode.Pattern = tool.Ptr("^[a-z]+$")
	def.Parameters = append(def.Parameters, mode)

	flag := tool.NewToolParameter("flag", tool.ParameterBoolean, "flag")
	flag.Required = false
	flag.Default = false
	def.Parameters = append(def.Parameters, flag)

	if err := def.Validate(); err != nil {
		t.Fatalf("definition rejected: %v", err)
	}

	got, err := def.InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	want := `{
		"type": "object",
		"properties": {
			"path": {"type":"string","description":"path to read"},
			"limit": {"type":"integer","description":"limit","minimum":1,"maximum":100,"default":10},
			"mode": {"type":"string","description":"mode","enum":["fast","slow"],"pattern":"^[a-z]+$"},
			"flag": {"type":"boolean","description":"flag","default":false}
		},
		"required": ["path"],
		"additionalProperties": false
	}`
	jsonEqual(t, got, want)
}

func TestDefinitionValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		def  tool.ToolDefinition
		want string
	}{
		{
			name: "invalid metadata",
			def: func() tool.ToolDefinition {
				def := baseDefinition("Bad")
				return def
			}(),
			want: "name must match",
		},
		{
			name: "duplicate parameter",
			def: func() tool.ToolDefinition {
				def := baseDefinition("demo")
				def.Parameters = append(def.Parameters, def.Parameters[0])
				return def
			}(),
			want: "duplicated",
		},
		{
			name: "invalid parameter",
			def: func() tool.ToolDefinition {
				def := baseDefinition("demo")
				def.Parameters[0].Description = ""
				return def
			}(),
			want: "description is required",
		},
		{
			name: "non-serializable parameter default",
			def: func() tool.ToolDefinition {
				def := baseDefinition("demo")
				p := tool.NewToolParameter("extra", tool.ParameterString, "extra")
				p.Required = false
				p.Default = func() {}
				def.Parameters = append(def.Parameters, p)
				return def
			}(),
			want: "not JSON-serializable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.def.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestToolResultValidate(t *testing.T) {
	tests := []struct {
		name   string
		result tool.ToolResult
		want   string
	}{
		{"success", tool.ToolResult{Success: true, Output: "ok"}, ""},
		{"success with error", tool.ToolResult{Success: true, Error: "boom"}, "must not contain"},
		{"failure without error", tool.ToolResult{Success: false}, "must contain an error"},
		{"failure", tool.ToolResult{Success: false, Error: "boom"}, ""},
		{"negative time", tool.ToolResult{Success: true, ExecutionTimeMS: tool.Ptr[int64](-1)}, "execution_time_ms"},
		{"negative tokens", tool.ToolResult{Success: true, TokensUsed: tool.Ptr(-1)}, "tokens_used"},
		{"non-serializable output", tool.ToolResult{Success: true, Output: make(chan int)}, "output is not JSON-serializable"},
		{"non-serializable metadata", tool.ToolResult{
			Success:  true,
			Metadata: map[string]any{"bad": make(chan int)},
		}, "metadata is not JSON-serializable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.result.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCurrencyConstants(t *testing.T) {
	if tool.CurrencyCNY != "CNY" {
		t.Fatalf("CurrencyCNY = %q, want CNY", tool.CurrencyCNY)
	}
	if tool.FenPerYuan != 100 {
		t.Fatalf("FenPerYuan = %d, want 100", tool.FenPerYuan)
	}
}

func TestParameterTypes(t *testing.T) {
	tests := []struct {
		name    string
		typ     tool.ToolParameterType
		valid   string
		invalid string
		want    string
	}{
		{"string", tool.ParameterString, `"x"`, `1`, "a string"},
		{"integer", tool.ParameterInteger, `42`, `1.5`, "an integer"},
		{"number", tool.ParameterFloat, `1.5`, `"1.5"`, "a number"},
		{"boolean", tool.ParameterBoolean, `true`, `"true"`, "a boolean"},
		{"array", tool.ParameterArray, `[1,2]`, `{}`, "an array"},
		{"object", tool.ParameterObject, `{"a":1}`, `[]`, "an object"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := tool.ToolDefinition{
				Metadata: baseMetadata("demo"),
				Parameters: []tool.ToolParameter{
					tool.NewToolParameter("value", tt.typ, "value"),
				},
			}
			if err := def.Validate(); err != nil {
				t.Fatalf("definition rejected: %v", err)
			}
			ft := &fakeTool{def: def}
			invoker := tool.NewInvoker()

			result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(fmt.Sprintf(`{"value":%s}`, tt.valid)))
			if err != nil || !result.Success {
				t.Fatalf("valid %s = (%+v, %v), want success", tt.name, result, err)
			}

			result, err = invoker.Invoke(context.Background(), ft, def, json.RawMessage(fmt.Sprintf(`{"value":%s}`, tt.invalid)))
			if err != nil {
				t.Fatalf("invalid %s error = %v, want nil", tt.name, err)
			}
			if result.Success || !strings.Contains(result.Error, tt.want) {
				t.Fatalf("invalid %s = %+v, want substring %q", tt.name, result, tt.want)
			}
		})
	}
}
