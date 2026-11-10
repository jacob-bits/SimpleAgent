package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"simpleagent/internal/agent"
	"simpleagent/internal/config"
)

func TestApplyOverridesAllFlags(t *testing.T) {
	args := []string{
		"-base-url", "http://example.com/v1",
		"-model", "override-model",
		"-temperature", "1.5",
		"-max-tokens", "42",
		"-llm-timeout", "7",
		"-reasoning-effort", "high",
		"-min-steps", "2",
		"-max-steps", "4",
		"-max-repeated-actions", "3",
		"-no-progress-threshold", "0.8",
		"-no-progress-min-chars", "10",
		"-max-history-chars", "1000",
		"-timeout", "9",
		"-verbose",
		"-allowed-root", "/tmp",
		"-max-file-bytes", "1234",
		"question",
	}

	var stdout, stderr bytes.Buffer
	parsed, code := parseFlags(args, &stdout, &stderr)
	if code != flagDone {
		t.Fatalf("parseFlags() code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	cfg := config.Default()
	applyOverrides(cfg, parsed.options, parsed.visited)

	if cfg.LLM.BaseURL != "http://example.com/v1" ||
		cfg.LLM.Model != "override-model" ||
		cfg.LLM.Temperature != 1.5 ||
		cfg.LLM.MaxTokens != 42 ||
		cfg.LLM.TimeoutSeconds != 7 ||
		cfg.LLM.ReasoningEffort != "high" {
		t.Fatalf("LLM config = %+v", cfg.LLM)
	}
	if cfg.Agent.MinSteps != 2 ||
		cfg.Agent.MaxSteps != 4 ||
		cfg.Agent.MaxRepeatedActions != 3 ||
		cfg.Agent.NoProgressThreshold != 0.8 ||
		cfg.Agent.NoProgressMinChars != 10 ||
		cfg.Agent.MaxHistoryChars != 1000 ||
		cfg.Agent.TimeoutSeconds != 9 ||
		!cfg.Agent.Verbose {
		t.Fatalf("Agent config = %+v", cfg.Agent)
	}
	if cfg.Tools.AllowedRoot != "/tmp" || cfg.Tools.MaxFileBytes != 1234 {
		t.Fatalf("Tools config = %+v", cfg.Tools)
	}
	if len(parsed.question) != 1 || parsed.question[0] != "question" {
		t.Fatalf("question = %+v", parsed.question)
	}
}

func TestApplyOverridesOnlyVisited(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Verbose = true

	var stdout, stderr bytes.Buffer
	parsed, code := parseFlags([]string{"-verbose=false", "question"}, &stdout, &stderr)
	if code != flagDone {
		t.Fatalf("parseFlags() code = %d", code)
	}
	applyOverrides(cfg, parsed.options, parsed.visited)
	if cfg.Agent.Verbose {
		t.Fatal("explicit -verbose=false did not override the config")
	}
	if cfg.LLM.Model != config.Default().LLM.Model || cfg.Tools.AllowedRoot != config.Default().Tools.AllowedRoot {
		t.Fatal("unvisited flags changed the config")
	}
}

func TestResolveQuestion(t *testing.T) {
	tests := []struct {
		name       string
		stdin      io.Reader
		positional []string
		want       string
		wantErr    bool
	}{
		{name: "positional", stdin: strings.NewReader("ignored"), positional: []string{"a", "b"}, want: "a b"},
		{name: "stdin", stdin: strings.NewReader("  from stdin\n"), want: "from stdin"},
		{name: "empty positional", positional: []string{"  "}, wantErr: true},
		{name: "nil stdin", wantErr: true},
		{name: "empty stdin", stdin: strings.NewReader(" "), wantErr: true},
		{name: "too large", stdin: strings.NewReader(strings.Repeat("x", maxQuestionBytes+1)), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveQuestion(tt.stdin, tt.positional)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveQuestion() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveQuestion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeError(t *testing.T) {
	got := sanitizeError("line1\nline2\t\x1b[31mred")
	if strings.ContainsAny(got, "\n\r\t\x1b") {
		t.Fatalf("sanitizeError() = %q, still contains control characters", got)
	}
	if !strings.Contains(got, "line1 line2") || !strings.Contains(got, "red") {
		t.Fatalf("sanitizeError() = %q", got)
	}
	if got := sanitizeError("\n\t\r"); got != "unknown error" {
		t.Fatalf("sanitizeError(control only) = %q", got)
	}
	long := sanitizeError(strings.Repeat("x", maxErrorRunes+50))
	if len([]rune(long)) != maxErrorRunes {
		t.Fatalf("sanitizeError(long) has %d runes, want %d", len([]rune(long)), maxErrorRunes)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestWriteAnswer(t *testing.T) {
	var b strings.Builder
	if err := writeAnswer(&b, "answer"); err != nil {
		t.Fatalf("writeAnswer() error = %v", err)
	}
	if b.String() != "answer\n" {
		t.Fatalf("writeAnswer() = %q", b.String())
	}

	b.Reset()
	if err := writeAnswer(&b, "answer\n"); err != nil {
		t.Fatalf("writeAnswer() error = %v", err)
	}
	if b.String() != "answer\n" {
		t.Fatalf("writeAnswer() = %q", b.String())
	}

	if err := writeAnswer(shortWriter{}, "answer"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAnswer(shortWriter) error = %v, want io.ErrShortWrite", err)
	}
}

func TestWarnMissingAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.APIKey = ""
	cfg.LLM.APIKeyEnv = "SIMPLEAGENT_TEST_API_KEY"
	t.Setenv("SIMPLEAGENT_TEST_API_KEY", "")

	var b strings.Builder
	warnMissingAPIKey(&b, cfg)
	if !strings.Contains(b.String(), "warning: no LLM API key configured") ||
		!strings.Contains(b.String(), "SIMPLEAGENT_TEST_API_KEY") {
		t.Fatalf("warning = %q", b.String())
	}

	cfg.LLM.APIKey = "set"
	b.Reset()
	warnMissingAPIKey(&b, cfg)
	if b.Len() != 0 {
		t.Fatalf("warning with key = %q, want empty", b.String())
	}
}

func TestTraceFormat(t *testing.T) {
	var b strings.Builder
	writeTrace(&b, agent.TraceEvent{
		Kind:        agent.TraceStep,
		Step:        1,
		Thought:     "line1\nline2",
		Action:      "read_file",
		ActionInput: `{"path":"notes.txt"}`,
	})
	writeTrace(&b, agent.TraceEvent{
		Kind:        agent.TraceTool,
		Step:        1,
		Action:      "read_file",
		Status:      "ok",
		Observation: `{"output":"content"}`,
	})
	writeTrace(&b, agent.TraceEvent{
		Kind:    agent.TraceControl,
		Step:    1,
		Control: `<control>continue</control>`,
	})
	writeTrace(&b, agent.TraceEvent{
		Kind:       agent.TraceStop,
		Step:       2,
		StopReason: agent.StopMaxSteps,
		Err:        errors.New("boom\nline"),
	})

	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("trace lines = %+v", lines)
	}
	if !strings.Contains(lines[0], "trace step=1") ||
		!strings.Contains(lines[0], `thought="line1\nline2"`) {
		t.Fatalf("step trace = %q", lines[0])
	}
	if !strings.Contains(lines[1], "trace tool step=1") || !strings.Contains(lines[1], "status=ok") {
		t.Fatalf("tool trace = %q", lines[1])
	}
	if !strings.Contains(lines[2], "trace control step=1") {
		t.Fatalf("control trace = %q", lines[2])
	}
	if !strings.Contains(lines[3], "trace stop reason=max_steps steps=2") ||
		!strings.Contains(lines[3], `error="boom line"`) {
		t.Fatalf("stop trace = %q", lines[3])
	}
}
