package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"simpleagent/internal/tool"
)

func validationDefinition() tool.ToolDefinition {
	def := baseDefinition("demo")

	limit := tool.NewToolParameter("limit", tool.ParameterInteger, "limit")
	limit.Required = false
	limit.Default = 10
	limit.MinValue = tool.Ptr(1.0)
	limit.MaxValue = tool.Ptr(100.0)
	def.Parameters = append(def.Parameters, limit)

	mode := tool.NewToolParameter("mode", tool.ParameterString, "mode")
	mode.Required = false
	mode.Enum = []any{"a", "b"}
	def.Parameters = append(def.Parameters, mode)

	return def
}

func TestInvokerAppliesDefaults(t *testing.T) {
	def := validationDefinition()
	var captured json.RawMessage
	ft := &fakeTool{
		def: def,
		exec: func(_ context.Context, input json.RawMessage) (tool.ToolResult, error) {
			captured = append(json.RawMessage(nil), input...)
			return tool.ToolResult{Success: true, Output: "ok"}, nil
		},
	}

	result, err := tool.NewInvoker().Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("Invoke error = %v", err)
	}
	if !result.Success {
		t.Fatalf("result = %+v", result)
	}
	jsonEqual(t, captured, `{"path":"x","limit":10}`)
}

func TestInvokerInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing required", `{}`, "missing required parameter"},
		{"wrong type", `{"path":1}`, "path must be a string"},
		{"unknown parameter", `{"path":"x","extra":1}`, "unknown parameter"},
		{"null value", `{"path":null}`, "must not be null"},
		{"fractional integer", `{"path":"x","limit":1.5}`, "must be an integer"},
		{"below minimum", `{"path":"x","limit":0}`, "must be >= 1"},
		{"above maximum", `{"path":"x","limit":101}`, "must be <= 100"},
		{"enum violation", `{"path":"x","mode":"c"}`, "must be one of"},
		{"top-level null", `null`, "action_input must be a JSON object"},
		{"top-level array", `[]`, "action_input must be a JSON object"},
		{"trailing object", `{"path":"x"} {"path":"y"}`, "exactly one"},
		{"null optional", `{"path":"x","limit":null}`, "must not be null"},
	}

	def := validationDefinition()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTool{def: def}
			result, err := tool.NewInvoker().Invoke(context.Background(), ft, def, json.RawMessage(tt.body))
			if err != nil {
				t.Fatalf("Invoke error = %v, want nil (model-visible failure)", err)
			}
			if result.Success {
				t.Fatal("Success = true, want false")
			}
			if !strings.Contains(result.Error, tt.want) {
				t.Fatalf("Error = %q, want substring %q", result.Error, tt.want)
			}
		})
	}
}

func TestInvokerRateLimit(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	invoker := tool.NewInvoker(tool.WithClock(func() time.Time { return now }))

	def := baseDefinition("demo")
	def.Metadata.RateLimit = tool.Ptr(1)
	ft := &fakeTool{def: def}

	if result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`)); err != nil || !result.Success {
		t.Fatalf("first call = (%+v, %v), want success", result, err)
	}

	result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "rate limit exceeded") {
		t.Fatalf("second call = %+v, want rate limit failure", result)
	}

	now = now.Add(61 * time.Second)
	if result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`)); err != nil || !result.Success {
		t.Fatalf("third call after window = (%+v, %v), want success", result, err)
	}
}

func TestInvokerBudget(t *testing.T) {
	def := baseDefinition("demo")
	def.Metadata.CostPerUse = 60
	ft := &fakeTool{def: def}
	budget := tool.NewBudget(100)
	invoker := tool.NewInvoker(tool.WithBudget(budget))

	if result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`)); err != nil || !result.Success {
		t.Fatalf("first call = (%+v, %v), want success", result, err)
	}
	if got := budget.SpentFen(); got != 60 {
		t.Fatalf("SpentFen() = %d, want 60", got)
	}

	result, err := invoker.Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "cost budget exceeded") {
		t.Fatalf("second call = %+v, want budget failure", result)
	}
	if got := budget.SpentFen(); got != 60 {
		t.Fatalf("SpentFen() after rejected call = %d, want 60", got)
	}
}

func TestBudgetCharge(t *testing.T) {
	budget := tool.NewBudget(100)
	if got := budget.LimitFen(); got != 100 {
		t.Fatalf("LimitFen() = %d, want 100", got)
	}
	if err := budget.Charge(60); err != nil {
		t.Fatalf("Charge(60): %v", err)
	}
	if err := budget.Charge(50); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("Charge(50) = %v, want budget exceeded", err)
	}
	if got := budget.SpentFen(); got != 60 {
		t.Fatalf("SpentFen() = %d, want 60", got)
	}
	if err := budget.Charge(-1); err == nil || !strings.Contains(err.Error(), ">= 0") {
		t.Fatalf("Charge(-1) = %v, want non-negative error", err)
	}

	unlimited := tool.NewBudget(-1)
	if err := unlimited.Charge(math.MaxInt64); err != nil {
		t.Fatalf("unlimited Charge(max): %v", err)
	}
	if err := unlimited.Charge(1); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("unlimited overflow Charge = %v, want overflow", err)
	}

	var nilBudget *tool.Budget
	if got := nilBudget.SpentFen(); got != 0 {
		t.Fatalf("nil SpentFen() = %d, want 0", got)
	}
	if got := nilBudget.LimitFen(); got != 0 {
		t.Fatalf("nil LimitFen() = %d, want 0", got)
	}
	if err := nilBudget.Charge(1); err != nil {
		t.Fatalf("nil Charge(1) = %v, want nil", err)
	}
}

func TestInvokerTimeout(t *testing.T) {
	def := baseDefinition("demo")
	def.Metadata.TimeoutSeconds = 1
	ft := &fakeTool{
		def: def,
		exec: func(ctx context.Context, _ json.RawMessage) (tool.ToolResult, error) {
			<-ctx.Done()
			return tool.ToolResult{}, ctx.Err()
		},
	}

	start := time.Now()
	_, err := tool.NewInvoker().Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Invoke error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("timeout returned after %v, want about 1s", elapsed)
	}
}

func TestInvokerContextCanceled(t *testing.T) {
	def := baseDefinition("demo")
	ft := &fakeTool{def: def}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.NewInvoker().Invoke(ctx, ft, def, json.RawMessage(`{"path":"x"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke error = %v, want context.Canceled", err)
	}
}

func TestInvokerInvalidResult(t *testing.T) {
	def := baseDefinition("demo")
	tests := []struct {
		name   string
		result tool.ToolResult
	}{
		{"success with error", tool.ToolResult{Success: true, Error: "boom"}},
		{"failure without error", tool.ToolResult{Success: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTool{
				def: def,
				exec: func(context.Context, json.RawMessage) (tool.ToolResult, error) {
					return tt.result, nil
				},
			}
			_, err := tool.NewInvoker().Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
			if err == nil || !strings.Contains(err.Error(), "invalid result") {
				t.Fatalf("Invoke error = %v, want invalid result", err)
			}
		})
	}
}

func TestInvokerNilTool(t *testing.T) {
	var ft tool.Tool
	_, err := tool.NewInvoker().Invoke(context.Background(), ft, tool.ToolDefinition{}, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "tool is nil") {
		t.Fatalf("Invoke error = %v, want nil tool", err)
	}
}

func TestInvokerFillsExecutionTime(t *testing.T) {
	def := baseDefinition("demo")
	ft := &fakeTool{
		def: def,
		exec: func(context.Context, json.RawMessage) (tool.ToolResult, error) {
			return tool.ToolResult{Success: true, Output: "ok"}, nil
		},
	}
	result, err := tool.NewInvoker().Invoke(context.Background(), ft, def, json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("Invoke error = %v", err)
	}
	if result.ExecutionTimeMS == nil || *result.ExecutionTimeMS < 0 {
		t.Fatalf("ExecutionTimeMS = %v, want non-negative value", result.ExecutionTimeMS)
	}
}
