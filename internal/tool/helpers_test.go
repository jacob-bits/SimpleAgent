package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"simpleagent/internal/tool"
)

type fakeTool struct {
	def  tool.ToolDefinition
	exec func(context.Context, json.RawMessage) (tool.ToolResult, error)
}

func (f *fakeTool) Definition() tool.ToolDefinition { return f.def }

func (f *fakeTool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	if f.exec != nil {
		return f.exec(ctx, input)
	}
	return tool.ToolResult{Success: true, Output: "ok"}, nil
}

func baseMetadata(name string) tool.ToolMetadata {
	m := tool.NewToolMetadata(name, "1.0.0", "test tool", tool.CategoryCalculation)
	m.Sandboxed = false
	return m
}

func baseDefinition(name string) tool.ToolDefinition {
	return tool.ToolDefinition{
		Metadata: baseMetadata(name),
		Parameters: []tool.ToolParameter{
			tool.NewToolParameter("path", tool.ParameterString, "path to read"),
		},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func jsonEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !deepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func deepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
