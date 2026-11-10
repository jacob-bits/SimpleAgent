package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	validAction = `{"thought":"read","action":"read_file","action_input":{"path":"notes.txt"}}`
	validFinal  = `{"thought":"done","final_answer":"Orion"}`
)

func TestParseStep(t *testing.T) {
	four := fenceMarker + fenceMarker[:1]

	tests := []struct {
		name string
		raw  string
		want Step
	}{
		{
			name: "bare action",
			raw:  validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "bare final",
			raw:  validFinal,
			want: Step{Thought: "done", FinalAnswer: "Orion"},
		},
		{
			name: "thought omitted",
			raw:  `{"action":"read_file","action_input":{"path":"notes.txt"}}`,
			want: Step{
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "empty thought",
			raw:  `{"thought":"","action":"read_file","action_input":{"path":"notes.txt"}}`,
			want: Step{
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "empty action input",
			raw:  `{"action":"read_file","action_input":{}}`,
			want: Step{Action: "read_file", ActionInput: json.RawMessage(`{}`)},
		},
		{
			name: "unknown tool accepted",
			raw:  `{"action":"write_file","action_input":{"path":"x"}}`,
			want: Step{Action: "write_file", ActionInput: json.RawMessage(`{"path":"x"}`)},
		},
		{
			name: "action whitespace preserved",
			raw:  `{"action":" read_file ","action_input":{}}`,
			want: Step{Action: " read_file ", ActionInput: json.RawMessage(`{}`)},
		},
		{
			name: "final whitespace preserved",
			raw:  `{"final_answer":"  Orion  "}`,
			want: Step{FinalAnswer: "  Orion  "},
		},
		{
			name: "unicode fields",
			raw:  `{"thought":"读取文件 📄","action":"read_file","action_input":{"path":"笔记.txt"}}`,
			want: Step{
				Thought:     "读取文件 📄",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"笔记.txt"}`),
			},
		},
		{
			name: "nested braces and quotes",
			raw:  `{"thought":"braces { } and quote \"","action":"read_file","action_input":{"path":"a}b\"c","meta":{"x":[1,2]}}}`,
			want: Step{
				Thought:     `braces { } and quote "`,
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"a}b\"c","meta":{"x":[1,2]}}`),
			},
		},
		{
			name: "action input compaction",
			raw: `{
  "action": "read_file",
  "action_input": {
    "path": "notes.txt",
    "meta": {"a": 1, "b": [1, 2]}
  }
}`,
			want: Step{
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt","meta":{"a":1,"b":[1,2]}}`),
			},
		},
		{
			name: "final with braces and newline",
			raw:  `{"thought":"done","final_answer":"line1\nline2 {x} \"q\" 项目"}`,
			want: Step{Thought: "done", FinalAnswer: "line1\nline2 {x} \"q\" 项目"},
		},
		{
			name: "trailing prose",
			raw:  validAction + "\nthanks",
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "leading prose",
			raw:  "Here is the step:\n" + validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "json fence",
			raw:  fenced("json", validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "untagged fence",
			raw:  fenced("", validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "uppercase json fence",
			raw:  fenced("JSON", validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "prose around fence",
			raw:  "intro\n" + fenced("json", validAction) + "\noutro",
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "multiple json fences first wins",
			raw:  fenced("json", validAction) + "\n" + fenced("json", validFinal),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "json fence over prose example",
			raw:  "example: " + validFinal + "\n" + fenced("json", validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "json fence over untagged fence",
			raw:  fenced("", validAction) + "\n" + fenced("json", validFinal),
			want: Step{Thought: "done", FinalAnswer: "Orion"},
		},
		{
			name: "unclosed fence",
			raw:  fenceMarker + "json\n" + validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "four backtick fence",
			raw:  four + "json\n" + validAction + "\n" + four,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "three leading spaces before fence",
			raw:  "intro\n   " + fenced("json", validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "CRLF fence",
			raw:  strings.ReplaceAll(fenced("json", validAction), "\n", "\r\n"),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "BOM before object",
			raw:  "\ufeff" + validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "BOM after spaces",
			raw:  "  \ufeff  " + validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "BOM inside fence",
			raw:  fenced("json", "\ufeff"+validAction),
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "CRLF around object",
			raw:  "\r\n" + validAction + "\r\n",
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "malformed candidate then valid object",
			raw:  "example {not json}\n" + validAction,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "first valid object wins",
			raw:  validAction + "\n" + validFinal,
			want: Step{
				Thought:     "read",
				Action:      "read_file",
				ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
			},
		},
		{
			name: "unknown action input parameter accepted",
			raw:  `{"action":"read_file","action_input":{"unknown":true}}`,
			want: Step{Action: "read_file", ActionInput: json.RawMessage(`{"unknown":true}`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStep(tt.raw)
			if err != nil {
				t.Fatalf("ParseStep() error = %v", err)
			}
			assertStep(t, got, tt.want)
		})
	}
}

func TestParseStepErrors(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantKind   ParseErrorKind
		wantDetail string
	}{
		{name: "empty response", raw: "", wantKind: ParseErrEmpty, wantDetail: "empty"},
		{name: "whitespace only", raw: "  \n\t  ", wantKind: ParseErrEmpty, wantDetail: "empty"},
		{name: "BOM only", raw: "\ufeff", wantKind: ParseErrEmpty, wantDetail: "empty"},
		{name: "too large", raw: strings.Repeat("a", MaxStepBytes+1), wantKind: ParseErrTooLarge, wantDetail: "limit"},
		{name: "invalid UTF-8", raw: string([]byte{0xff}), wantKind: ParseErrInvalidUTF8, wantDetail: "UTF-8"},
		{name: "no object", raw: "hello", wantKind: ParseErrNoObject, wantDetail: "no JSON object"},
		{name: "invalid fragment", raw: "{not json}", wantKind: ParseErrInvalidJSON, wantDetail: "no valid JSON object"},
		{name: "unclosed object", raw: `{"action":"read_file"`, wantKind: ParseErrInvalidJSON, wantDetail: "no valid JSON object"},
		{name: "only opening brace", raw: "{", wantKind: ParseErrInvalidJSON, wantDetail: "no valid JSON object"},
		{name: "array", raw: "[]", wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "array wrapped action", raw: `[{"action":"read_file","action_input":{}}]`, wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "array with trailing prose", raw: `[{"action":"read_file","action_input":{}}] done`, wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "JSON string", raw: ` "hello" `, wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "JSON number", raw: "42", wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "JSON bool", raw: "true", wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "JSON null", raw: "null", wantKind: ParseErrNotObject, wantDetail: "top-level value"},
		{name: "conflict", raw: `{"action":"read_file","final_answer":"x","action_input":{}}`, wantKind: ParseErrConflict, wantDetail: "mutually exclusive"},
		{name: "conflict with empty action", raw: `{"action":"","final_answer":"x"}`, wantKind: ParseErrConflict, wantDetail: "mutually exclusive"},
		{name: "missing outcome", raw: "{}", wantKind: ParseErrMissingOutcome, wantDetail: "exactly one"},
		{name: "missing outcome with thought", raw: `{"thought":"x"}`, wantKind: ParseErrMissingOutcome, wantDetail: "exactly one"},
		{name: "missing outcome with input", raw: `{"action_input":{}}`, wantKind: ParseErrMissingOutcome, wantDetail: "exactly one"},
		{name: "empty action", raw: `{"action":"  ","action_input":{}}`, wantKind: ParseErrEmptyAction, wantDetail: "must not be empty"},
		{name: "empty action and missing input", raw: `{"action":""}`, wantKind: ParseErrEmptyAction, wantDetail: "must not be empty"},
		{name: "empty final", raw: `{"final_answer":"  "}`, wantKind: ParseErrEmptyFinal, wantDetail: "must not be empty"},
		{name: "empty final with input", raw: `{"final_answer":" ","action_input":{}}`, wantKind: ParseErrEmptyFinal, wantDetail: "must not be empty"},
		{name: "missing action input", raw: `{"action":"read_file"}`, wantKind: ParseErrMissingInput, wantDetail: "required"},
		{name: "null action input", raw: `{"action":"read_file","action_input":null}`, wantKind: ParseErrInvalidInput, wantDetail: "JSON object"},
		{name: "array action input", raw: `{"action":"read_file","action_input":[]}`, wantKind: ParseErrInvalidInput, wantDetail: "JSON object"},
		{name: "string action input", raw: `{"action":"read_file","action_input":"x"}`, wantKind: ParseErrInvalidInput, wantDetail: "JSON object"},
		{name: "number action input", raw: `{"action":"read_file","action_input":1}`, wantKind: ParseErrInvalidInput, wantDetail: "JSON object"},
		{name: "unexpected final input", raw: `{"final_answer":"ok","action_input":{}}`, wantKind: ParseErrUnexpectedInput, wantDetail: "not allowed"},
		{name: "unknown field", raw: `{"finalanswer":"x"}`, wantKind: ParseErrUnknownField, wantDetail: "unknown field"},
		{name: "duplicate field", raw: `{"action":"read_file","action":"write_file","action_input":{}}`, wantKind: ParseErrDuplicateField, wantDetail: "duplicate top-level key"},
		{name: "duplicate in prose stops search", raw: "example " + `{"action":"read_file","action":"write_file","action_input":{}}` + " actual " + validAction, wantKind: ParseErrDuplicateField, wantDetail: "duplicate top-level key"},
		{name: "thought null", raw: `{"thought":null,"final_answer":"x"}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "thought number", raw: `{"thought":1,"final_answer":"x"}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "action object", raw: `{"action":{},"action_input":{}}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "action number", raw: `{"action":1,"action_input":{}}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "final array", raw: `{"final_answer":[]}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "final number", raw: `{"final_answer":1}`, wantKind: ParseErrFieldType, wantDetail: "must be a string"},
		{name: "field-invalid first object stops search", raw: `{"action":"read_file"}` + "\n" + validAction, wantKind: ParseErrMissingInput, wantDetail: "required"},
		{name: "unknown-field first object stops search", raw: `{"unexpected":true}` + "\n" + validAction, wantKind: ParseErrUnknownField, wantDetail: "unknown field"},
		{name: "invalid-input first object stops search", raw: `{"action":"read_file","action_input":null}` + "\n" + validAction, wantKind: ParseErrInvalidInput, wantDetail: "JSON object"},
		{name: "invalid selected fence does not fall back", raw: fenced("json", "not json") + "\n" + validAction, wantKind: ParseErrNoObject, wantDetail: "no JSON object"},
		{name: "invalid selected fence object does not fall back", raw: fenced("json", "{bad}") + "\n" + validAction, wantKind: ParseErrInvalidJSON, wantDetail: "no valid JSON object"},
		{name: "array fence does not fall back", raw: fenced("", "[]") + "\n" + validAction, wantKind: ParseErrNotObject, wantDetail: "top-level value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStep(tt.raw)
			if err == nil {
				t.Fatal("ParseStep() error = nil, want error")
			}

			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type = %T, want *ParseError", err)
			}
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatal("error does not wrap ErrInvalidOutput")
			}
			if pe.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q (detail: %s)", pe.Kind, tt.wantKind, pe.Detail)
			}
			if tt.wantDetail != "" && !strings.Contains(pe.Detail, tt.wantDetail) {
				t.Fatalf("Detail = %q, want substring %q", pe.Detail, tt.wantDetail)
			}
			if len([]rune(pe.Snippet)) > MaxErrorSnippetRunes {
				t.Fatalf("Snippet has %d runes, want <= %d", len([]rune(pe.Snippet)), MaxErrorSnippetRunes)
			}
			if len([]rune(pe.Detail)) > MaxErrorDetailRunes {
				t.Fatalf("Detail has %d runes, want <= %d", len([]rune(pe.Detail)), MaxErrorDetailRunes)
			}
			if !strings.Contains(err.Error(), string(tt.wantKind)) {
				t.Fatalf("Error() = %q, want kind %q", err.Error(), tt.wantKind)
			}
		})
	}
}

func TestUnknownFieldsDetail(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "\"extra%d\":1,", i)
	}
	b.WriteString("\"action\":\"read_file\",\"action_input\":{}}")

	_, err := ParseStep(b.String())
	if err == nil {
		t.Fatal("ParseStep() error = nil, want error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error type = %T, want *ParseError", err)
	}
	if pe.Kind != ParseErrUnknownField {
		t.Fatalf("Kind = %q, want %q", pe.Kind, ParseErrUnknownField)
	}
	if strings.Count(pe.Detail, "\"extra") != maxUnknownFields {
		t.Fatalf("Detail = %q, want %d shown fields", pe.Detail, maxUnknownFields)
	}
	if !strings.Contains(pe.Detail, "(2 more)") {
		t.Fatalf("Detail = %q, want omitted count", pe.Detail)
	}
}

func TestParseErrorBounds(t *testing.T) {
	_, err := ParseStep(strings.Repeat("a", 600))
	if err == nil {
		t.Fatal("ParseStep() error = nil, want error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error type = %T, want *ParseError", err)
	}
	if len([]rune(pe.Snippet)) != MaxErrorSnippetRunes {
		t.Fatalf("Snippet has %d runes, want %d", len([]rune(pe.Snippet)), MaxErrorSnippetRunes)
	}
	if !strings.HasSuffix(pe.Snippet, "...") {
		t.Fatalf("Snippet = %q, want ellipsis suffix", pe.Snippet)
	}

	longDetail := newParseError(ParseErrUnknownField, strings.Repeat("x", MaxErrorDetailRunes+100), "source")
	if len([]rune(longDetail.Detail)) != MaxErrorDetailRunes {
		t.Fatalf("Detail has %d runes, want %d", len([]rune(longDetail.Detail)), MaxErrorDetailRunes)
	}
	if !strings.HasSuffix(longDetail.Detail, "...") {
		t.Fatalf("Detail = %q, want ellipsis suffix", longDetail.Detail)
	}
}

func TestParseError(t *testing.T) {
	var nilErr *ParseError
	if got := nilErr.Error(); got != ErrInvalidOutput.Error() {
		t.Fatalf("nil ParseError.Error() = %q, want %q", got, ErrInvalidOutput.Error())
	}

	err := &ParseError{Kind: ParseErrNoObject, Detail: "bad", Snippet: "line1\nline2"}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Fatalf("Error() contains raw newline: %q", msg)
	}
	if !strings.Contains(msg, `raw="line1\nline2"`) {
		t.Fatalf("Error() = %q, want quoted snippet", msg)
	}
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatal("ParseError does not match ErrInvalidOutput")
	}

	emptyMsg := (&ParseError{}).Error()
	if emptyMsg != ErrInvalidOutput.Error() {
		t.Fatalf("empty ParseError.Error() = %q, want %q", emptyMsg, ErrInvalidOutput.Error())
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
		want  string
	}{
		{name: "empty", value: "", max: 5, want: ""},
		{name: "shorter", value: "abc", max: 5, want: "abc"},
		{name: "exact", value: "abcde", max: 5, want: "abcde"},
		{name: "long", value: "abcdef", max: 5, want: "ab..."},
		{name: "max three", value: "abcdef", max: 3, want: "abc"},
		{name: "max one", value: "ab", max: 1, want: "a"},
		{name: "max zero", value: "abc", max: 0, want: ""},
		{name: "multibyte", value: "你好世界啊", max: 4, want: "你..."},
		{name: "long multibyte", value: strings.Repeat("界", 600), max: 512, want: strings.Repeat("界", 509) + "..."},
		{name: "exact bound", value: strings.Repeat("x", 512), max: 512, want: strings.Repeat("x", 512)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.value, tt.max)
			if got != tt.want {
				t.Fatalf("truncateRunes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFences(t *testing.T) {
	fence := fenceMarker
	four := fence + fence[:1]

	open, info, ok := parseFenceOpen("   " + fence + "JSON")
	if !ok || open != 3 || info != "JSON" {
		t.Fatalf("parseFenceOpen() = (%d, %q, %v), want (3, JSON, true)", open, info, ok)
	}
	if _, _, ok := parseFenceOpen("    " + fence + "json"); ok {
		t.Fatal("parseFenceOpen() accepted four leading spaces")
	}
	if !isFenceClose("   "+fence+"   ", 3) {
		t.Fatal("isFenceClose() rejected a valid closing fence")
	}
	if isFenceClose(fence+" text", 3) {
		t.Fatal("isFenceClose() accepted trailing text")
	}
	if isFenceClose(fence[:2], 3) {
		t.Fatal("isFenceClose() accepted too few backticks")
	}
	if _, ok := trimFenceIndent("    text"); ok {
		t.Fatal("trimFenceIndent() accepted four leading spaces")
	}

	if blocks := scanFences("no fence"); blocks != nil {
		t.Fatalf("scanFences(no fence) = %v, want nil", blocks)
	}
	if blocks := scanFences("~~~json\nbody\n~~~"); blocks != nil {
		t.Fatalf("scanFences(tilde fence) = %v, want nil", blocks)
	}

	blocks := scanFences(fence + "json\n" + validAction + "\n" + fence)
	if len(blocks) != 1 || blocks[0].info != "json" || blocks[0].body != validAction {
		t.Fatalf("scanFences(json) = %+v", blocks)
	}

	blocks = scanFences(four + "json\n" + validAction + "\n" + four)
	if len(blocks) != 1 || blocks[0].info != "json" || blocks[0].body != validAction {
		t.Fatalf("scanFences(four backticks) = %+v", blocks)
	}

	blocks = scanFences(fence + "json\n" + validAction)
	if len(blocks) != 1 || blocks[0].info != "json" || blocks[0].body != validAction {
		t.Fatalf("scanFences(unclosed) = %+v", blocks)
	}

	blocks = scanFences(fence + "json\n" + validAction + "\n" + fence + "   ")
	if len(blocks) != 1 || blocks[0].body != validAction {
		t.Fatalf("scanFences(trailing spaces) = %+v", blocks)
	}

	crlf := strings.ReplaceAll(fence+"json\n"+validAction+"\n"+fence, "\n", "\r\n")
	blocks = scanFences(crlf)
	if len(blocks) != 1 || blocks[0].info != "json" || strings.TrimSpace(blocks[0].body) != validAction {
		t.Fatalf("scanFences(CRLF) = %+v", blocks)
	}

	two := fence + "json\n" + validAction + "\n" + fence + "\n" + fence + "text\nbody\n" + fence
	blocks = scanFences(two)
	if len(blocks) != 2 || blocks[0].info != "json" || blocks[1].info != "text" || blocks[1].body != "body" {
		t.Fatalf("scanFences(two blocks) = %+v", blocks)
	}
}

func TestSelectPayload(t *testing.T) {
	fence := fenceMarker
	plain := "prefix " + validAction

	if got := selectPayload(plain); got != plain {
		t.Fatalf("selectPayload(no fence) = %q, want original", got)
	}

	text := "example " + validFinal + "\n" + fence + "json\n" + validAction + "\n" + fence
	if got := selectPayload(text); got != validAction {
		t.Fatalf("selectPayload(json fence) = %q, want action", got)
	}

	text = fence + "\n" + validAction + "\n" + fence
	if got := selectPayload(text); got != validAction {
		t.Fatalf("selectPayload(untagged fence) = %q, want action", got)
	}

	text = fence + "text\n" + validAction + "\n" + fence
	if got := selectPayload(text); got != text {
		t.Fatalf("selectPayload(other tag) = %q, want original", got)
	}

	text = fence + "\nnot json\n" + fence + "\n" + validAction
	if got := selectPayload(text); got != text {
		t.Fatalf("selectPayload(non-container untagged) = %q, want original", got)
	}

	text = fence + "json\n\n" + fence
	if got := selectPayload(text); got != "" {
		t.Fatalf("selectPayload(empty json fence) = %q, want empty", got)
	}

	text = fence + "\n" + validFinal + "\n" + fence + "\n" + fence + "json\n" + validAction + "\n" + fence
	if got := selectPayload(text); got != validAction {
		t.Fatalf("selectPayload(json over untagged) = %q, want action", got)
	}
}

func TestDecodeObject(t *testing.T) {
	fields, err := decodeObject(`{"a":1,"b":{"c":2}}`)
	if err != nil {
		t.Fatalf("decodeObject() error = %v", err)
	}
	if len(fields) != 2 || string(fields["a"]) != "1" || string(fields["b"]) != `{"c":2}` {
		t.Fatalf("decodeObject() fields = %#v", fields)
	}

	if _, err := decodeObject("{}"); err != nil {
		t.Fatalf("decodeObject({}) error = %v", err)
	}
	if _, err := decodeObject("[]"); !errors.Is(err, errNotObject) {
		t.Fatalf("decodeObject([]) error = %v, want errNotObject", err)
	}
	if _, err := decodeObject(`{"a":1,"a":2}`); !errors.Is(err, errDuplicateKey) {
		t.Fatalf("decodeObject(duplicate) error = %v, want errDuplicateKey", err)
	}
	if _, err := decodeObject(`{"a":1} trailing`); err == nil {
		t.Fatal("decodeObject(trailing) error = nil, want error")
	}
	if _, err := decodeObject("{"); err == nil {
		t.Fatal("decodeObject(unclosed) error = nil, want error")
	}
}

func TestMatchObject(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		start int
		end   int
		ok    bool
	}{
		{name: "simple", raw: `{"a":1}`, start: 0, end: 7, ok: true},
		{name: "nested", raw: `{"a":{"b":1}}`, start: 0, end: 13, ok: true},
		{name: "brace in string", raw: `{"s":"}"}`, start: 0, end: 9, ok: true},
		{name: "escaped quote", raw: `{"s":"a\"}b"}`, start: 0, end: 13, ok: true},
		{name: "trailing text", raw: `{"a":1} tail`, start: 0, end: 7, ok: true},
		{name: "unclosed", raw: `{"a":1`, start: 0, end: 6, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			end, ok := matchObject(tt.raw, tt.start)
			if end != tt.end || ok != tt.ok {
				t.Fatalf("matchObject() = (%d, %v), want (%d, %v)", end, ok, tt.end, tt.ok)
			}
		})
	}
}

func TestObjectAndStringHelpers(t *testing.T) {
	input, err := compactObjectField("action_input", json.RawMessage("{\n  \"path\": \"x\"\n}"))
	if err != nil {
		t.Fatalf("compactObjectField() error = %v", err)
	}
	if string(input) != `{"path":"x"}` {
		t.Fatalf("compactObjectField() = %q", input)
	}
	if _, err := compactObjectField("action_input", json.RawMessage("[]")); err == nil {
		t.Fatal("compactObjectField(array) error = nil")
	}
	if _, err := compactObjectField("action_input", json.RawMessage("{")); err == nil {
		t.Fatal("compactObjectField(malformed) error = nil")
	}

	value, err := decodeStringField("action", json.RawMessage(`"read_file"`))
	if err != nil || value != "read_file" {
		t.Fatalf("decodeStringField() = (%q, %v)", value, err)
	}
	if _, err := decodeStringField("action", json.RawMessage("null")); err == nil {
		t.Fatal("decodeStringField(null) error = nil")
	}
	if _, err := decodeStringField("action", json.RawMessage(`"unterminated`)); err == nil {
		t.Fatal("decodeStringField(malformed) error = nil")
	}
}

func fenced(info, body string) string {
	opening := fenceMarker
	if info != "" {
		opening += info
	}
	return opening + "\n" + body + "\n" + fenceMarker
}

func assertStep(t *testing.T, got, want Step) {
	t.Helper()
	if got.Thought != want.Thought {
		t.Errorf("Thought = %q, want %q", got.Thought, want.Thought)
	}
	if got.Action != want.Action {
		t.Errorf("Action = %q, want %q", got.Action, want.Action)
	}
	if got.FinalAnswer != want.FinalAnswer {
		t.Errorf("FinalAnswer = %q, want %q", got.FinalAnswer, want.FinalAnswer)
	}
	if string(got.ActionInput) != string(want.ActionInput) {
		t.Errorf("ActionInput = %q, want %q", got.ActionInput, want.ActionInput)
	}
	if (got.ActionInput == nil) != (want.ActionInput == nil) {
		t.Errorf("ActionInput nil = %v, want %v", got.ActionInput == nil, want.ActionInput == nil)
	}
}

func FuzzParseStep(f *testing.F) {
	f.Add(validAction)
	f.Add(validFinal)
	f.Add(fenced("json", validAction))
	f.Add("prefix " + validFinal + "\n" + validAction)
	f.Add(`{"action":"read_file","final_answer":"x","action_input":{}}`)
	f.Add("")
	f.Add("[]")
	f.Add(string([]byte{0xff}))

	f.Fuzz(func(t *testing.T, raw string) {
		step, err := ParseStep(raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("error does not wrap ErrInvalidOutput: %v", err)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type = %T, want *ParseError", err)
			}
			if len([]rune(pe.Snippet)) > MaxErrorSnippetRunes {
				t.Fatalf("Snippet has %d runes, want <= %d", len([]rune(pe.Snippet)), MaxErrorSnippetRunes)
			}
			if len([]rune(pe.Detail)) > MaxErrorDetailRunes {
				t.Fatalf("Detail has %d runes, want <= %d", len([]rune(pe.Detail)), MaxErrorDetailRunes)
			}
			_ = err.Error()
			return
		}

		if (step.Action == "") == (step.FinalAnswer == "") {
			t.Fatalf("exactly one outcome required: %+v", step)
		}
		if step.Action != "" {
			if len(step.ActionInput) == 0 || step.ActionInput[0] != '{' {
				t.Fatalf("action without object input: %+v", step)
			}
			if !json.Valid(step.ActionInput) {
				t.Fatalf("action input is not valid JSON: %+v", step)
			}
		}
		if step.FinalAnswer != "" && step.ActionInput != nil {
			t.Fatalf("final with input: %+v", step)
		}
	})
}
