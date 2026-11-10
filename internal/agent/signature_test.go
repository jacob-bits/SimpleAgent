package agent

import (
	"encoding/json"
	"testing"
)

func TestCanonicalSignature(t *testing.T) {
	first, err := canonicalSignature("read_file", json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("canonicalSignature() error = %v", err)
	}
	second, err := canonicalSignature("read_file", json.RawMessage(`{ "a": 1, "b": 2 }`))
	if err != nil {
		t.Fatalf("canonicalSignature() error = %v", err)
	}
	if first != second {
		t.Fatalf("signatures differ: %q != %q", first, second)
	}

	one, _ := canonicalSignature("read_file", json.RawMessage(`{"n":1}`))
	oneFloat, _ := canonicalSignature("read_file", json.RawMessage(`{"n":1.0}`))
	if one != oneFloat {
		t.Fatalf("number signatures differ: %q != %q", one, oneFloat)
	}

	differentAction, _ := canonicalSignature("write_file", json.RawMessage(`{"a":1}`))
	if differentAction == first {
		t.Fatal("different actions produced the same signature")
	}
	differentInput, _ := canonicalSignature("read_file", json.RawMessage(`{"a":2}`))
	if differentInput == first {
		t.Fatal("different inputs produced the same signature")
	}
}

func TestCanonicalSignatureError(t *testing.T) {
	if _, err := canonicalSignature("read_file", json.RawMessage("{")); err == nil {
		t.Fatal("canonicalSignature(invalid) error = nil")
	}
	if _, err := canonicalSignature("read_file", nil); err == nil {
		t.Fatal("canonicalSignature(nil) error = nil")
	}
}
