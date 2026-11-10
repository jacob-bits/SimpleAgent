package readfile_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"simpleagent/internal/tool"
	"simpleagent/internal/tool/readfile"
)

func newTestTool(t *testing.T, maxBytes int64) (*readfile.Tool, string) {
	t.Helper()
	root := t.TempDir()
	tr, err := readfile.New(root, maxBytes)
	if err != nil {
		t.Fatalf("New(%q, %d): %v", root, maxBytes, err)
	}
	return tr, root
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func bodyForPath(path string) string {
	raw, _ := json.Marshal(map[string]string{"path": path})
	return string(raw)
}

func execute(t *testing.T, tr *readfile.Tool, body string) (tool.ToolResult, error) {
	t.Helper()
	return tr.Execute(context.Background(), json.RawMessage(body))
}

func checkSuccess(t *testing.T, result tool.ToolResult, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if !result.Success {
		t.Fatalf("Success = false, Error = %q", result.Error)
	}
	if result.Error != "" {
		t.Fatalf("Error = %q, want empty", result.Error)
	}
	output, ok := result.Output.(string)
	if !ok {
		t.Fatalf("Output type = %T, want string", result.Output)
	}
	if output != want {
		t.Fatalf("Output = %q, want %q", output, want)
	}
}

func requireSuccess(t *testing.T, tr *readfile.Tool, body, want string) {
	t.Helper()
	result, err := execute(t, tr, body)
	checkSuccess(t, result, err, want)
}

func requireFailure(t *testing.T, tr *readfile.Tool, body, want string) {
	t.Helper()
	result, err := execute(t, tr, body)
	if err != nil {
		t.Fatalf("Execute error = %v, want nil (model-visible failure)", err)
	}
	if result.Success {
		t.Fatalf("Success = true, want false")
	}
	if !strings.Contains(result.Error, want) {
		t.Fatalf("Error = %q, want substring %q", result.Error, want)
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestNewValidation(t *testing.T) {
	root := t.TempDir()

	if _, err := readfile.New("", 1024); err == nil {
		t.Fatal("New with empty root = nil error")
	}
	if _, err := readfile.New(root, 0); err == nil {
		t.Fatal("New with zero max bytes = nil error")
	}
	if _, err := readfile.New(filepath.Join(root, "missing"), 1024); err == nil {
		t.Fatal("New with missing root = nil error")
	}

	file := filepath.Join(root, "file.txt")
	writeTestFile(t, file, "x")
	if _, err := readfile.New(file, 1024); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("New(file) error = %v, want not a directory", err)
	}

	if _, err := readfile.New(root, 8<<20); err == nil || !strings.Contains(err.Error(), "exceeds memory_limit_mb") {
		t.Fatalf("New(large max) error = %v, want memory limit error", err)
	}
	if _, err := readfile.New(root, 7<<20); err != nil {
		t.Fatalf("New(7MiB) error = %v, want success", err)
	}
}

func TestNewRootSymlink(t *testing.T) {
	target := t.TempDir()
	writeTestFile(t, filepath.Join(target, "notes.txt"), "inside")

	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	tr, err := readfile.New(link, 1024)
	if err != nil {
		t.Fatalf("New(symlink root): %v", err)
	}
	requireSuccess(t, tr, bodyForPath("notes.txt"), "inside")
}

func TestDefinition(t *testing.T) {
	tr, _ := newTestTool(t, 1024)
	def := tr.Definition()

	if def.Metadata.Name != "read_file" || def.Metadata.Version != "1.0.0" {
		t.Fatalf("identity = %+v", def.Metadata)
	}
	if def.Metadata.Category != tool.CategoryFile {
		t.Fatalf("category = %q, want file", def.Metadata.Category)
	}
	if def.Metadata.RequiresAuth || def.Metadata.Dangerous {
		t.Fatalf("requires_auth/dangerous = true, want false")
	}
	if def.Metadata.Sandboxed {
		t.Fatal("sandboxed = true, want false")
	}
	if def.Metadata.RateLimit == nil || *def.Metadata.RateLimit != 60 {
		t.Fatalf("rate_limit = %v, want 60", def.Metadata.RateLimit)
	}
	if def.Metadata.TimeoutSeconds != 10 || def.Metadata.MemoryLimitMB != 16 {
		t.Fatalf("timeout/memory = %d/%d, want 10/16", def.Metadata.TimeoutSeconds, def.Metadata.MemoryLimitMB)
	}
	if def.Metadata.CostPerUse != 0 {
		t.Fatalf("cost_per_use = %d, want 0", def.Metadata.CostPerUse)
	}
	if len(def.Parameters) != 1 {
		t.Fatalf("parameters = %d, want 1", len(def.Parameters))
	}
	p := def.Parameters[0]
	if p.Name != "path" || p.Type != tool.ParameterString || !p.Required {
		t.Fatalf("path parameter = %+v", p)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Definition.Validate(): %v", err)
	}

	schema, err := def.InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	assertJSONEqual(t, schema, `{
		"type":"object",
		"properties":{"path":{"type":"string","description":"相对于 allowed_root 的路径，或 allowed_root 内的绝对路径"}},
		"required":["path"],
		"additionalProperties":false
	}`)
}

func TestExecuteSuccess(t *testing.T) {
	tr, root := newTestTool(t, 1024)

	content := "hello 世界\n"
	writeTestFile(t, filepath.Join(root, "notes.txt"), content)

	result, err := execute(t, tr, bodyForPath("notes.txt"))
	checkSuccess(t, result, err, content)
	if got := result.Metadata["bytes_read"]; got != len(content) {
		t.Fatalf("bytes_read = %v, want %d", got, len(content))
	}

	writeTestFile(t, filepath.Join(root, "empty.txt"), "")
	requireSuccess(t, tr, bodyForPath("empty.txt"), "")

	abs := filepath.Join(root, "notes.txt")
	requireSuccess(t, tr, bodyForPath(abs), content)

	crlf := "\xef\xbb\xbfa\r\nb"
	writeTestFile(t, filepath.Join(root, "crlf.txt"), crlf)
	requireSuccess(t, tr, bodyForPath("crlf.txt"), crlf)
}

func TestExecutePathSafety(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(root, "notes.txt"), "inside")
	writeTestFile(t, filepath.Join(root, "sub", "a.txt"), "nested")
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "outside")

	tr, err := readfile.New(root, 1024)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	requireSuccess(t, tr, bodyForPath("notes.txt"), "inside")
	requireSuccess(t, tr, bodyForPath("sub/a.txt"), "nested")
	requireSuccess(t, tr, bodyForPath(filepath.Join(root, "notes.txt")), "inside")

	insideLink := filepath.Join(root, "good-link")
	if err := os.Symlink(filepath.Join(root, "notes.txt"), insideLink); err == nil {
		requireSuccess(t, tr, bodyForPath("good-link"), "inside")
	}

	outsideFile := filepath.Join(outside, "secret.txt")
	relOutside, err := filepath.Rel(root, outsideFile)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	requireFailure(t, tr, bodyForPath(relOutside), "path escapes allowed_root")
	requireFailure(t, tr, bodyForPath(outsideFile), "path escapes allowed_root")
	requireFailure(t, tr, bodyForPath(filepath.Join("sub", "..", relOutside)), "path escapes allowed_root")

	outsideLink := filepath.Join(root, "leak-link")
	if err := os.Symlink(outsideFile, outsideLink); err == nil {
		requireFailure(t, tr, bodyForPath("leak-link"), "path escapes allowed_root")
	}

	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err == nil {
		requireFailure(t, tr, bodyForPath("dangling"), "open file: no such file or directory")
	}

	requireFailure(t, tr, bodyForPath("sub"), "not a regular file")
	requireFailure(t, tr, bodyForPath("."), "not a regular file")
	requireFailure(t, tr, bodyForPath("missing.txt"), "open file: no such file or directory")

	evil := root + "-evil"
	writeTestFile(t, filepath.Join(evil, "secret.txt"), "evil")
	t.Cleanup(func() { os.RemoveAll(evil) })
	requireFailure(t, tr, bodyForPath(filepath.Join(evil, "secret.txt")), "path escapes allowed_root")
}

func TestExecuteSizeAndEncoding(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "exact.txt"), "12345678")
	writeTestFile(t, filepath.Join(root, "too-big.txt"), "123456789")
	if err := os.WriteFile(filepath.Join(root, "invalid.bin"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatalf("write invalid.bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nul.bin"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatalf("write nul.bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nul-invalid.bin"), []byte{0x00, 0xff}, 0o644); err != nil {
		t.Fatalf("write nul-invalid.bin: %v", err)
	}

	tr, err := readfile.New(root, 8)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	requireSuccess(t, tr, bodyForPath("exact.txt"), "12345678")
	requireFailure(t, tr, bodyForPath("too-big.txt"), "file exceeds max_file_bytes")
	requireFailure(t, tr, bodyForPath("invalid.bin"), "file is not valid UTF-8 text")
	requireFailure(t, tr, bodyForPath("nul.bin"), "file appears to be binary")
	requireFailure(t, tr, bodyForPath("nul-invalid.bin"), "file appears to be binary")
}

func TestExecuteInputDefense(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	writeTestFile(t, filepath.Join(root, "notes.txt"), "inside")

	requireFailure(t, tr, `{}`, "path is required")
	requireFailure(t, tr, `{"path":""}`, "path is required")
	requireFailure(t, tr, `{"path":"   "}`, "path is required")

	tests := []string{
		`{"path":"notes.txt","extra":1}`,
		`{"path":"notes.txt"} {}`,
		`[]`,
		`{"path":123}`,
		`not-json`,
	}
	for _, body := range tests {
		if _, err := execute(t, tr, body); err == nil {
			t.Fatalf("Execute(%s) error = nil, want internal decode error", body)
		}
	}
}

func TestExecuteZeroValue(t *testing.T) {
	var tr readfile.Tool
	_, err := tr.Execute(context.Background(), json.RawMessage(`{"path":"notes.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("Execute error = %v, want not initialized", err)
	}
}

func TestExecuteCanceled(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	writeTestFile(t, filepath.Join(root, "notes.txt"), "inside")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tr.Execute(ctx, json.RawMessage(bodyForPath("notes.txt")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
}

func TestExecuteReadOnly(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	path := filepath.Join(root, "notes.txt")
	writeTestFile(t, path, "inside")

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	requireSuccess(t, tr, bodyForPath("notes.txt"), "inside")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("modtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(data) != "inside" {
		t.Fatalf("content changed: %q", data)
	}
}

func TestExecuteConcurrent(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	writeTestFile(t, filepath.Join(root, "notes.txt"), "inside")

	const calls = 16
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := tr.Execute(context.Background(), json.RawMessage(bodyForPath("notes.txt")))
			if err != nil {
				errs <- err
				return
			}
			if !result.Success {
				errs <- errors.New(result.Error)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestRegistryIntegration(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	writeTestFile(t, filepath.Join(root, "notes.txt"), "inside")

	registry, err := tool.NewRegistry(tr)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"notes.txt"}`))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !result.Success || result.Output != "inside" {
		t.Fatalf("result = %+v", result)
	}
	if result.ExecutionTimeMS == nil {
		t.Fatal("ExecutionTimeMS = nil, want filled by Invoker")
	}

	result, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("missing path error = %v, want nil", err)
	}
	if result.Success || !strings.Contains(result.Error, "missing required parameter") {
		t.Fatalf("missing path result = %+v", result)
	}
}
