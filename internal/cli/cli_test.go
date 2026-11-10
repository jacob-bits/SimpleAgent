package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	cliReadAction  = `{"thought":"read","action":"read_file","action_input":{"path":"notes.txt"}}`
	cliFinalAnswer = `{"thought":"done","final_answer":"Orion"}`
)

type mockChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type mockLLM struct {
	server     *httptest.Server
	mu         sync.Mutex
	requests   []mockChatRequest
	responses  []string
	statusCode int
	block      bool
}

func newMockLLM(t *testing.T, responses ...string) *mockLLM {
	t.Helper()
	m := &mockLLM{
		responses:  responses,
		statusCode: http.StatusOK,
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockLLM) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var request mockChatRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	m.requests = append(m.requests, request)
	index := len(m.requests) - 1
	m.mu.Unlock()

	if m.block {
		<-r.Context().Done()
		return
	}
	if m.statusCode != http.StatusOK {
		w.WriteHeader(m.statusCode)
		fmt.Fprint(w, "{\"error\":{\"message\":\"mock failure\"}}")
		return
	}
	if index >= len(m.responses) {
		http.Error(w, "no scripted response", http.StatusInternalServerError)
		return
	}

	content, err := json.Marshal(m.responses[index])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w,
		"{\"id\":\"mock\",\"model\":\"mock\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":\"stop\"}]}",
		content,
	)
}

func (m *mockLLM) requestsSnapshot() []mockChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mockChatRequest(nil), m.requests...)
}

func runCLI(ctx context.Context, args []string, stdin string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(ctx, args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeCLIFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

func cliArgs(serverURL, root string, extra ...string) []string {
	args := []string{
		"-base-url", serverURL,
		"-allowed-root", root,
		"-model", "test-model",
	}
	return append(args, extra...)
}

func TestRunHelp(t *testing.T) {
	code, stdout, stderr := runCLI(context.Background(), []string{"-h"}, "")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"Usage: agent", "-llm-timeout", "stdin", "-max-history-chars"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %s", want, stdout)
		}
	}
}

func TestRunUnknownFlag(t *testing.T) {
	code, stdout, stderr := runCLI(context.Background(), []string{"-does-not-exist"}, "")
	if code != exitUse {
		t.Fatalf("exit = %d, want %d", code, exitUse)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "Usage: agent") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	code, stdout, stderr := runCLI(context.Background(), []string{"-config", path, "question"}, "")
	if code != exitUse {
		t.Fatalf("exit = %d, want %d", code, exitUse)
	}
	if stdout != "" || !strings.Contains(stderr, "error:") {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	path := writeCLIFile(t, t.TempDir(), "config.json", "{bad")
	code, _, stderr := runCLI(context.Background(), []string{"-config", path, "question"}, "")
	if code != exitUse || !strings.Contains(stderr, "error:") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestRunFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "min steps", args: []string{"-min-steps", "0", "question"}},
		{name: "max steps", args: []string{"-max-steps", "0", "question"}},
		{name: "temperature", args: []string{"-temperature", "3", "question"}},
		{name: "base url", args: []string{"-base-url", "not a url", "question"}},
		{name: "max file bytes", args: []string{"-max-file-bytes", "0", "question"}},
		{name: "min above max", args: []string{"-min-steps", "3", "-max-steps", "2", "question"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(context.Background(), tt.args, "")
			if code != exitUse || stdout != "" || !strings.Contains(stderr, "error:") {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
			}
		})
	}
}

func TestRunInvalidAllowedRoot(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	missing := filepath.Join(t.TempDir(), "missing-root")
	code, stdout, stderr := runCLI(context.Background(), []string{"-allowed-root", missing, "question"}, "")
	if code != exitUse || stdout != "" || !strings.Contains(stderr, "error:") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestRunEmptyQuestion(t *testing.T) {
	code, stdout, stderr := runCLI(context.Background(), nil, "")
	if code != exitUse || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "question") || !strings.Contains(stderr, "Usage: agent") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunStdinTooLarge(t *testing.T) {
	code, _, stderr := runCLI(context.Background(), nil, strings.Repeat("x", maxQuestionBytes+1))
	if code != exitUse || !strings.Contains(stderr, "exceeds") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestRunTerminalStdinWithoutQuestion(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("Open(os.DevNull) error = %v", err)
	}
	defer devNull.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), nil, devNull, &stdout, &stderr)
	if code != exitUse || stdout.Len() != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "question") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunSuccessEndToEnd(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	writeCLIFile(t, root, "notes.txt", "项目代号：Orion\n主要风险：支付联调延期。\n")
	mock := newMockLLM(t, cliReadAction, cliFinalAnswer)

	args := cliArgs(mock.server.URL, root, "项目代号是什么？")
	code, stdout, stderr := runCLI(context.Background(), args, "")
	if code != exitOK || stdout != "Orion\n" || stderr != "" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}

	requests := mock.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].Model != "test-model" {
		t.Fatalf("model = %q", requests[0].Model)
	}
	if requests[0].Messages[1].Content != "项目代号是什么？" {
		t.Fatalf("question = %q", requests[0].Messages[1].Content)
	}
	if len(requests[1].Messages) != 4 {
		t.Fatalf("second messages = %+v", requests[1].Messages)
	}
	observation := requests[1].Messages[3].Content
	if !strings.Contains(observation, "status=\"ok\"") ||
		!strings.Contains(observation, "path=\"notes.txt\"") ||
		!strings.Contains(observation, "项目代号：Orion") {
		t.Fatalf("observation = %q", observation)
	}
}

func TestRunVerboseTrace(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	writeCLIFile(t, root, "notes.txt", "项目代号：Orion\n")
	mock := newMockLLM(t, cliReadAction, cliFinalAnswer)

	args := cliArgs(mock.server.URL, root, "-verbose", "项目代号是什么？")
	code, stdout, stderr := runCLI(context.Background(), args, "")
	if code != exitOK || stdout != "Orion\n" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for _, want := range []string{
		"trace step=1",
		"trace tool step=1",
		"trace stop reason=final_answer",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q: %s", want, stderr)
		}
	}
}

func TestRunQuestionFromStdin(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)

	args := cliArgs(mock.server.URL, root)
	code, stdout, stderr := runCLI(context.Background(), args, "from stdin\n")
	if code != exitOK || stdout != "Orion\n" || stderr != "" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	requests := mock.requestsSnapshot()
	if len(requests) != 1 || requests[0].Messages[1].Content != "from stdin" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestRunPositionalQuestionPrecedesStdin(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)

	args := cliArgs(mock.server.URL, root, "from arg")
	code, _, stderr := runCLI(context.Background(), args, "from stdin")
	if code != exitOK || stderr != "" {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	requests := mock.requestsSnapshot()
	if len(requests) != 1 || requests[0].Messages[1].Content != "from arg" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestRunConfigOverride(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	configPath := writeCLIFile(t, t.TempDir(), "config.json", `{
  "llm": {
    "base_url": "http://example.invalid",
    "model": "from-config",
    "api_key": "test-key"
  },
  "agent": {
    "verbose": true
  }
}`)
	mock := newMockLLM(t, cliFinalAnswer)

	args := []string{
		"-config", configPath,
		"-base-url", mock.server.URL,
		"-model", "from-flag",
		"-allowed-root", root,
		"-verbose=false",
		"question",
	}
	code, stdout, stderr := runCLI(context.Background(), args, "")
	if code != exitOK || stdout != "Orion\n" || stderr != "" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	requests := mock.requestsSnapshot()
	if len(requests) != 1 || requests[0].Model != "from-flag" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestRunOutOfRootObservation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeCLIFile(t, parent, "secret.txt", "secret")
	action := `{"thought":"read","action":"read_file","action_input":{"path":"../secret.txt"}}`
	mock := newMockLLM(t, action, cliFinalAnswer)

	args := cliArgs(mock.server.URL, root, "question")
	code, stdout, stderr := runCLI(context.Background(), args, "")
	if code != exitOK || stdout != "Orion\n" || stderr != "" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	requests := mock.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	observation := requests[1].Messages[3].Content
	if !strings.Contains(observation, "status=\"error\"") ||
		!strings.Contains(observation, "path=\"../secret.txt\"") ||
		!strings.Contains(observation, "path escapes allowed_root") {
		t.Fatalf("observation = %q", observation)
	}
}

func TestRunLLMError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t)
	mock.statusCode = http.StatusInternalServerError

	code, stdout, stderr := runCLI(context.Background(), cliArgs(mock.server.URL, root, "question"), "")
	if code != exitRun || stdout != "" || !strings.Contains(stderr, "error:") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestRunInvalidModelOutput(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, "not json")

	code, stdout, stderr := runCLI(context.Background(), cliArgs(mock.server.URL, root, "question"), "")
	if code != exitRun || stdout != "" || !strings.Contains(stderr, "error:") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestRunCanceled(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code, stdout, stderr := runCLI(ctx, cliArgs(mock.server.URL, root, "question"), "")
	if code != exitRun || stdout != "" || !strings.Contains(stderr, "canceled") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if len(mock.requestsSnapshot()) != 0 {
		t.Fatalf("requests = %d, want 0", len(mock.requestsSnapshot()))
	}
}

func TestRunTimeout(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)
	mock.block = true
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	code, stdout, stderr := runCLI(ctx, cliArgs(mock.server.URL, root, "question"), "")
	if code != exitRun || stdout != "" || !strings.Contains(stderr, "timeout") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write(p []byte) (int, error) {
	return 0, w.err
}

func TestRunWriteAnswerError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)
	var stderr bytes.Buffer
	code := Run(
		context.Background(),
		cliArgs(mock.server.URL, root, "question"),
		strings.NewReader(""),
		failingWriter{err: errors.New("stdout broken")},
		&stderr,
	)
	if code != exitRun || !strings.Contains(stderr.String(), "write answer") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunMissingAPIKeyWarning(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	root := t.TempDir()
	mock := newMockLLM(t, cliFinalAnswer)

	code, stdout, stderr := runCLI(context.Background(), cliArgs(mock.server.URL, root, "question"), "")
	if code != exitOK || stdout != "Orion\n" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "warning: no LLM API key configured") {
		t.Fatalf("stderr = %q, want warning", stderr)
	}
}
