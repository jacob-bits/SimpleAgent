package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"simpleagent/internal/config"
	"simpleagent/internal/llm"
	"simpleagent/internal/tool"
)

const (
	readAction  = `{"thought":"read","action":"read_file","action_input":{"path":"notes.txt"}}`
	finalAnswer = `{"thought":"done","final_answer":"Orion"}`
)

type completionCall struct {
	messages []llm.Message
}

type scriptedCompleter struct {
	responses []string
	errs      []error
	calls     []completionCall
}

func (c *scriptedCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	c.calls = append(c.calls, completionCall{
		messages: append([]llm.Message(nil), messages...),
	})
	index := len(c.calls) - 1
	if index < len(c.errs) && c.errs[index] != nil {
		return "", c.errs[index]
	}
	if index < len(c.responses) {
		return c.responses[index], nil
	}
	return "", errors.New("scripted completer exhausted")
}

type blockingCompleter struct {
	calls int
}

func (b *blockingCompleter) Complete(ctx context.Context, _ []llm.Message) (string, error) {
	b.calls++
	<-ctx.Done()
	return "", ctx.Err()
}

type registryCall struct {
	name  string
	input []byte
}

type scriptedRegistry struct {
	defs    []tool.ToolDefinition
	results []tool.ToolResult
	errs    []error
	calls   []registryCall
}

func (r *scriptedRegistry) Definitions() []tool.ToolDefinition {
	return r.defs
}

func (r *scriptedRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (tool.ToolResult, error) {
	r.calls = append(r.calls, registryCall{
		name:  name,
		input: append([]byte(nil), input...),
	})
	index := len(r.calls) - 1
	if index < len(r.errs) && r.errs[index] != nil {
		return tool.ToolResult{}, r.errs[index]
	}
	if index < len(r.results) {
		return r.results[index], nil
	}
	return tool.ToolResult{}, errors.New("scripted registry exhausted")
}

func testDefinition() tool.ToolDefinition {
	meta := tool.NewToolMetadata("read_file", "1.0.0", "Read a text file", tool.CategoryFile)
	meta.Sandboxed = false
	return tool.ToolDefinition{
		Metadata: meta,
		Parameters: []tool.ToolParameter{
			tool.NewToolParameter("path", tool.ParameterString, "file path"),
		},
	}
}

func testConfig() config.AgentConfig {
	return config.AgentConfig{
		MinSteps:            1,
		MaxSteps:            8,
		MaxRepeatedActions:  2,
		NoProgressThreshold: 0.95,
		NoProgressMinChars:  80,
		MaxHistoryChars:     200000,
		TimeoutSeconds:      5,
	}
}

func newTestAgent(t *testing.T, cfg config.AgentConfig, completer Completer, registry tool.Registry, opts ...Option) *Agent {
	t.Helper()
	agent, err := New(cfg, completer, registry, opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return agent
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, target)
	}
}

func TestRunTwoStepSuccess(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{readAction, finalAnswer}}
	registry := &scriptedRegistry{
		defs:    []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{{Success: true, Output: "Orion is the codename"}},
	}
	agent := newTestAgent(t, testConfig(), completer, registry)

	result, err := agent.Run(context.Background(), "What is the codename?")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Answer != "Orion" || result.Steps != 2 || result.Reason != StopFinalAnswer {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 2 {
		t.Fatalf("completer calls = %d, want 2", len(completer.calls))
	}
	if len(registry.calls) != 1 {
		t.Fatalf("registry calls = %d, want 1", len(registry.calls))
	}
	if registry.calls[0].name != "read_file" || string(registry.calls[0].input) != `{"path":"notes.txt"}` {
		t.Fatalf("registry call = %+v", registry.calls[0])
	}

	first := completer.calls[0].messages
	if len(first) != 2 || first[0].Role != "system" || first[1].Role != "user" {
		t.Fatalf("first messages = %+v", first)
	}
	second := completer.calls[1].messages
	if len(second) != 4 || second[2].Role != "assistant" || second[3].Role != "user" {
		t.Fatalf("second messages = %+v", second)
	}
	observation := second[3].Content
	if !strings.HasPrefix(observation, `<observation tool="read_file" path="notes.txt" status="ok">`) {
		t.Fatalf("observation prefix = %q", observation)
	}
	if !strings.Contains(observation, `{"output":"Orion is the codename"}`) {
		t.Fatalf("observation = %q", observation)
	}
	if !strings.HasSuffix(observation, "</observation>") {
		t.Fatalf("observation suffix = %q", observation)
	}
}

func TestRunFinalOneStep(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{finalAnswer}}
	registry := &scriptedRegistry{defs: []tool.ToolDefinition{testDefinition()}}
	agent := newTestAgent(t, testConfig(), completer, registry)

	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Answer != "Orion" || result.Steps != 1 || result.Reason != StopFinalAnswer {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 1 || len(registry.calls) != 0 {
		t.Fatalf("calls = completer %d, registry %d", len(completer.calls), len(registry.calls))
	}
}

func TestRunMinStepsDefersFinal(t *testing.T) {
	cfg := testConfig()
	cfg.MinSteps = 2
	completer := &scriptedCompleter{responses: []string{finalAnswer, finalAnswer}}
	registry := &scriptedRegistry{defs: []tool.ToolDefinition{testDefinition()}}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Answer != "Orion" || result.Steps != 2 || result.Reason != StopFinalAnswer {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 2 || len(registry.calls) != 0 {
		t.Fatalf("calls = completer %d, registry %d", len(completer.calls), len(registry.calls))
	}
	second := completer.calls[1].messages
	if len(second) != 4 {
		t.Fatalf("second messages = %+v", second)
	}
	control := second[3].Content
	if !strings.HasPrefix(control, `<control reason="min_steps">`) ||
		!strings.Contains(control, "至少完成 2 轮后才能结束") {
		t.Fatalf("control message = %q", control)
	}
}

func TestRunMaxStepsSkipsLastAction(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSteps = 2
	cfg.NoProgressThreshold = 0
	completer := &scriptedCompleter{responses: []string{readAction, readAction}}
	registry := &scriptedRegistry{
		defs:    []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{{Success: true, Output: "content"}},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	requireErrorIs(t, err, ErrMaxSteps)
	if result.Steps != 2 || result.Reason != StopMaxSteps {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 2 {
		t.Fatalf("completer calls = %d, want 2", len(completer.calls))
	}
	if len(registry.calls) != 1 {
		t.Fatalf("registry calls = %d, want 1 (last action must not execute)", len(registry.calls))
	}
}

func TestRunSingleStepMaxDoesNotExecuteTool(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSteps = 1
	completer := &scriptedCompleter{responses: []string{readAction}}
	registry := &scriptedRegistry{defs: []tool.ToolDefinition{testDefinition()}}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	requireErrorIs(t, err, ErrMaxSteps)
	if result.Steps != 1 || result.Reason != StopMaxSteps {
		t.Fatalf("Run() = %+v", result)
	}
	if len(registry.calls) != 0 {
		t.Fatalf("registry calls = %d, want 0", len(registry.calls))
	}
}

func TestRunRepeatedAction(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRepeatedActions = 2
	cfg.NoProgressThreshold = 0
	completer := &scriptedCompleter{responses: []string{readAction, readAction, readAction}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "one"},
			{Success: true, Output: "two"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	requireErrorIs(t, err, ErrRepeatedAction)
	if result.Steps != 3 || result.Reason != StopRepeated {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 3 {
		t.Fatalf("completer calls = %d, want 3", len(completer.calls))
	}
	if len(registry.calls) != 2 {
		t.Fatalf("registry calls = %d, want 2", len(registry.calls))
	}
}

func TestRunRepeatedActionCanonicalizesInput(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRepeatedActions = 2
	cfg.NoProgressThreshold = 0
	completer := &scriptedCompleter{responses: []string{
		`{"action":"read_file","action_input":{"path":"a","n":1}}`,
		`{"action":"read_file","action_input":{ "n": 1.0, "path": "a" }}`,
		`{"action":"read_file","action_input":{"n":1,"path":"a"}}`,
	}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "one"},
			{Success: true, Output: "two"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	_, err := agent.Run(context.Background(), "question")
	requireErrorIs(t, err, ErrRepeatedAction)
	if len(registry.calls) != 2 {
		t.Fatalf("registry calls = %d, want 2", len(registry.calls))
	}
}

func TestRunDifferentActionResetsRepeat(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRepeatedActions = 2
	cfg.NoProgressThreshold = 0
	completer := &scriptedCompleter{responses: []string{
		`{"action":"read_file","action_input":{"path":"a"}}`,
		`{"action":"read_file","action_input":{"path":"b"}}`,
		`{"action":"read_file","action_input":{"path":"a"}}`,
		finalAnswer,
	}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "one"},
			{Success: true, Output: "two"},
			{Success: true, Output: "three"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Steps != 4 || result.Reason != StopFinalAnswer || len(registry.calls) != 3 {
		t.Fatalf("Run() = %+v, registry calls = %d", result, len(registry.calls))
	}
}

func TestRunNoProgress(t *testing.T) {
	cfg := testConfig()
	cfg.NoProgressThreshold = 0.9
	cfg.NoProgressMinChars = 0
	completer := &scriptedCompleter{responses: []string{
		`{"action":"read_file","action_input":{"path":"a"}}`,
		`{"action":"read_file","action_input":{"path":"b"}}`,
		finalAnswer,
	}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "identical content"},
			{Success: true, Output: "identical content"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	requireErrorIs(t, err, ErrNoProgress)
	if result.Steps != 2 || result.Reason != StopNoProgress {
		t.Fatalf("Run() = %+v", result)
	}
	if len(completer.calls) != 2 || len(registry.calls) != 2 {
		t.Fatalf("calls = completer %d, registry %d", len(completer.calls), len(registry.calls))
	}
}

func TestRunNoProgressDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.NoProgressThreshold = 0
	cfg.NoProgressMinChars = 0
	completer := &scriptedCompleter{responses: []string{
		`{"action":"read_file","action_input":{"path":"a"}}`,
		`{"action":"read_file","action_input":{"path":"b"}}`,
		finalAnswer,
	}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "same"},
			{Success: true, Output: "same"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Steps != 3 || result.Reason != StopFinalAnswer || len(registry.calls) != 2 {
		t.Fatalf("Run() = %+v, registry calls = %d", result, len(registry.calls))
	}
}

func TestRunShortDifferentObservationsDoNotStop(t *testing.T) {
	cfg := testConfig()
	cfg.NoProgressThreshold = 0.5
	cfg.NoProgressMinChars = 80
	completer := &scriptedCompleter{responses: []string{
		`{"action":"read_file","action_input":{"path":"a"}}`,
		`{"action":"read_file","action_input":{"path":"b"}}`,
		finalAnswer,
	}}
	registry := &scriptedRegistry{
		defs: []tool.ToolDefinition{testDefinition()},
		results: []tool.ToolResult{
			{Success: true, Output: "ok"},
			{Success: true, Output: "done"},
		},
	}
	agent := newTestAgent(t, cfg, completer, registry)

	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Steps != 3 || len(registry.calls) != 2 {
		t.Fatalf("Run() = %+v, registry calls = %d", result, len(registry.calls))
	}
}
