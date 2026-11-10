// Package readfile implements the read_file tool: a read-only, UTF-8 text
// file reader constrained to a single allowed root.
package readfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"simpleagent/internal/tool"
)

const (
	toolName        = "read_file"
	toolVersion     = "1.0.0"
	toolDescription = "Read a UTF-8 text file and return its complete contents. " +
		"The path must be relative to the allowed root or an absolute path inside it. " +
		"The tool is read-only and rejects binary, oversized, or out-of-root files."

	rateLimitPerMinute = 60
	timeoutSeconds     = 10
	memoryLimitMB      = 16

	// memoryOverheadBytes covers the read buffer and the string conversion.
	memoryOverheadBytes int64 = 1 << 20
)

var (
	errPathRequired = errors.New("path is required")
	errFileTooLarge = errors.New("file exceeds max_file_bytes")
)

// Tool is the read_file implementation.
type Tool struct {
	root     string
	maxBytes int64
	def      tool.ToolDefinition
}

// New creates a read_file tool. The allowed root is resolved and verified at
// construction time so configuration mistakes fail at startup.
func New(allowedRoot string, maxFileBytes int64) (*Tool, error) {
	if strings.TrimSpace(allowedRoot) == "" {
		return nil, errors.New("allowed root is required")
	}
	if maxFileBytes <= 0 {
		return nil, errors.New("max file bytes must be > 0")
	}

	abs, err := filepath.Abs(allowedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve allowed root: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve allowed root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat allowed root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("allowed root is not a directory")
	}

	meta := tool.NewToolMetadata(toolName, toolVersion, toolDescription, tool.CategoryFile)
	rate := rateLimitPerMinute
	meta.RateLimit = &rate
	meta.TimeoutSeconds = timeoutSeconds
	meta.MemoryLimitMB = memoryLimitMB
	meta.Sandboxed = false
	meta.Dangerous = false
	meta.RequiresAuth = false
	meta.CostPerUse = 0

	// The declared memory budget must cover the file buffer and the string
	// conversion. Reject configurations that could exceed it.
	if maxFileBytes > (math.MaxInt64-memoryOverheadBytes)/2 {
		return nil, errors.New("max file bytes is too large")
	}
	needed := 2*maxFileBytes + memoryOverheadBytes
	budget := int64(meta.MemoryLimitMB) * (1 << 20)
	if budget < needed {
		return nil, fmt.Errorf(
			"max_file_bytes %d exceeds memory_limit_mb %d",
			maxFileBytes, meta.MemoryLimitMB,
		)
	}

	def := tool.ToolDefinition{
		Metadata: meta,
		Parameters: []tool.ToolParameter{
			tool.NewToolParameter(
				"path",
				tool.ParameterString,
				"相对于 allowed_root 的路径，或 allowed_root 内的绝对路径",
			),
		},
	}
	if err := def.Validate(); err != nil {
		return nil, fmt.Errorf("invalid read_file definition: %w", err)
	}

	return &Tool{root: root, maxBytes: maxFileBytes, def: def}, nil
}

// Definition returns the immutable tool definition.
func (t *Tool) Definition() tool.ToolDefinition {
	def := t.def
	def.Parameters = append([]tool.ToolParameter(nil), t.def.Parameters...)
	if def.Metadata.RateLimit != nil {
		rate := *def.Metadata.RateLimit
		def.Metadata.RateLimit = &rate
	}
	return def
}

// Execute reads one file and returns a normalized ToolResult.
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.ToolResult{}, err
	}
	if t == nil || t.root == "" || t.maxBytes <= 0 {
		return tool.ToolResult{}, errors.New("read_file tool is not initialized")
	}

	in, err := decodeInput(input)
	if err != nil {
		if errors.Is(err, errPathRequired) {
			return tool.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return tool.ToolResult{}, err
	}

	real, err := t.resolve(in.Path)
	if err != nil {
		return tool.ToolResult{Success: false, Error: err.Error()}, nil
	}

	data, err := t.readFile(real)
	if err != nil {
		return tool.ToolResult{Success: false, Error: err.Error()}, nil
	}

	return tool.ToolResult{
		Success: true,
		Output:  data,
		Metadata: map[string]any{
			"bytes_read": len(data),
		},
	}, nil
}

type input struct {
	Path string `json:"path"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var in input
	if err := dec.Decode(&in); err != nil {
		return input{}, fmt.Errorf("decode input: %w", err)
	}

	var trailing json.RawMessage
	switch err := dec.Decode(&trailing); {
	case err == nil:
		return input{}, errors.New("decode input: trailing data")
	case !errors.Is(err, io.EOF):
		return input{}, fmt.Errorf("decode input: %w", err)
	}

	if strings.TrimSpace(in.Path) == "" {
		return input{}, errPathRequired
	}
	return in, nil
}

func (t *Tool) readFile(path string) (string, error) {
	// Pre-check: reject directories, FIFOs and devices before opening. Opening
	// a FIFO for reading can block indefinitely.
	pre, err := os.Lstat(path)
	if err != nil {
		return "", openErr(err)
	}
	if !pre.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if pre.Size() > t.maxBytes {
		return "", errFileTooLarge
	}

	f, err := os.Open(path)
	if err != nil {
		return "", openErr(err)
	}
	defer f.Close()

	// Re-check after opening to narrow the stat/open race window.
	fi, err := f.Stat()
	if err != nil {
		return "", errors.New("stat file: cannot access file")
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if fi.Size() > t.maxBytes {
		return "", errFileTooLarge
	}

	data, err := readLimited(f, t.maxBytes)
	switch {
	case errors.Is(err, errFileTooLarge):
		return "", errFileTooLarge
	case err != nil:
		return "", errors.New("read file: I/O error")
	}

	// NUL is checked first because it is a strong binary signal. A NUL byte
	// is valid UTF-8, so the UTF-8 check alone would not catch it.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("file appears to be binary")
	}
	if !utf8.Valid(data) {
		return "", errors.New("file is not valid UTF-8 text")
	}
	return string(data), nil
}

func (t *Tool) resolve(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errPathRequired
	}

	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.root, candidate)
	}
	candidate = filepath.Clean(candidate)

	// First check: lexical containment, without touching the filesystem.
	if !withinRoot(t.root, candidate) {
		return "", errors.New("path escapes allowed_root")
	}

	// Second check: resolve symlinks, then re-check the real path.
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", openErr(err)
	}
	if !withinRoot(t.root, real) {
		return "", errors.New("path escapes allowed_root")
	}
	return real, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func openErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errors.New("open file: no such file or directory")
	case errors.Is(err, fs.ErrPermission):
		return errors.New("open file: permission denied")
	default:
		return errors.New("open file: cannot access file")
	}
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errFileTooLarge
	}
	return data, nil
}
