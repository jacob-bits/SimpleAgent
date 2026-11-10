//go:build unix

package readfile_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"simpleagent/internal/tool/readfile"
)

func TestExecutePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}

	root := t.TempDir()
	path := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(path, []byte("inside"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	tr, err := readfile.New(root, 1024)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := tr.Execute(context.Background(), []byte(`{"path":"secret.txt"}`))
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if result.Success || result.Error != "open file: permission denied" {
		t.Fatalf("result = %+v, want permission denied", result)
	}
}
