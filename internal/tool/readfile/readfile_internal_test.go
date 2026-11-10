package readfile

import (
	"errors"
	"io/fs"
	"math"
	"strings"
	"testing"
)

func TestReadLimited(t *testing.T) {
	data, err := readLimited(strings.NewReader("12345678"), 8)
	if err != nil {
		t.Fatalf("readLimited exact: %v", err)
	}
	if string(data) != "12345678" {
		t.Fatalf("readLimited exact = %q", data)
	}

	if _, err := readLimited(strings.NewReader("123456789"), 8); !errors.Is(err, errFileTooLarge) {
		t.Fatalf("readLimited over limit error = %v, want errFileTooLarge", err)
	}

	data, err = readLimited(strings.NewReader(""), 8)
	if err != nil || len(data) != 0 {
		t.Fatalf("readLimited empty = (%q, %v)", data, err)
	}

	data, err = readLimited(strings.NewReader("abc"), math.MaxInt64)
	if err != nil || string(data) != "abc" {
		t.Fatalf("readLimited max int = (%q, %v)", data, err)
	}
}

func TestWithinRoot(t *testing.T) {
	tests := []struct {
		root string
		path string
		want bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/a/b/../c", false},
		{"/a/b", "/a/b/../b/c", true},
	}
	for _, tt := range tests {
		if got := withinRoot(tt.root, tt.path); got != tt.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", tt.root, tt.path, got, tt.want)
		}
	}
}

func TestOpenErrClassification(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fs.ErrNotExist, "open file: no such file or directory"},
		{fs.ErrPermission, "open file: permission denied"},
		{errors.New("boom"), "open file: cannot access file"},
	}
	for _, tt := range tests {
		if got := openErr(tt.err).Error(); got != tt.want {
			t.Errorf("openErr(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
