//go:build unix

package readfile_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"simpleagent/internal/tool"
)

func TestExecuteFIFODoesNotBlock(t *testing.T) {
	tr, root := newTestTool(t, 1024)
	path := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	done := make(chan struct{})
	var result tool.ToolResult
	var execErr error
	go func() {
		result, execErr = tr.Execute(context.Background(), json.RawMessage(bodyForPath("pipe")))
		close(done)
	}()

	select {
	case <-done:
		if execErr != nil {
			t.Fatalf("Execute error = %v, want nil", execErr)
		}
		if result.Success {
			t.Fatal("Success = true, want false")
		}
		if !strings.Contains(result.Error, "not a regular file") {
			t.Fatalf("Error = %q, want not a regular file", result.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute blocked on FIFO")
	}
}
