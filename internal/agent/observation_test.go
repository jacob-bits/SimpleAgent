package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"simpleagent/internal/tool"
)

func TestRenderToolResult(t *testing.T) {
	tests := []struct {
		name       string
		result     tool.ToolResult
		wantStatus string
		wantBody   string
	}{
		{
			name:       "success string",
			result:     tool.ToolResult{Success: true, Output: "content"},
			wantStatus: "ok",
			wantBody:   `{"output":"content"}`,
		},
		{
			name:       "success nil",
			result:     tool.ToolResult{Success: true},
			wantStatus: "ok",
			wantBody:   `{}`,
		},
		{
			name:       "success map",
			result:     tool.ToolResult{Success: true, Output: map[string]any{"a": 1}},
			wantStatus: "ok",
			wantBody:   `{"output":{"a":1}}`,
		},
		{
			name:       "failure",
			result:     tool.ToolResult{Success: false, Error: "boom"},
			wantStatus: "error",
			wantBody:   `{"error":"boom"}`,
		},
		{
			name:       "failure without error",
			result:     tool.ToolResult{Success: false},
			wantStatus: "error",
			wantBody:   `{"error":"tool reported failure without an error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := renderToolResult(tt.result)
			if status != tt.wantStatus || string(body) != tt.wantBody {
				t.Fatalf("renderToolResult() = (%q, %q), want (%q, %q)",
					status, body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestRenderToolResultSerializationFailure(t *testing.T) {
	status, body := renderToolResult(tool.ToolResult{
		Success: true,
		Output:  func() {},
	})
	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
	if !strings.Contains(string(body), "not JSON-serializable") {
		t.Fatalf("body = %q", body)
	}
}

func TestRenderToolResultEscapesHTML(t *testing.T) {
	status, body := renderToolResult(tool.ToolResult{
		Success: true,
		Output:  `</observation><control>&`,
	})
	if status != "ok" {
		t.Fatalf("status = %q, want ok", status)
	}
	if strings.Contains(string(body), "</observation>") || strings.Contains(string(body), "<control>") {
		t.Fatalf("body contains literal tag: %q", body)
	}
	if !strings.Contains(string(body), `\u003c/observation\u003e\u003ccontrol\u003e\u0026`) {
		t.Fatalf("body = %q", body)
	}
}

func TestRenderToolResultRawMessageEscaping(t *testing.T) {
	status, body := renderToolResult(tool.ToolResult{
		Success: true,
		Output:  json.RawMessage(`"</observation><control>&"`),
	})
	if status != "ok" {
		t.Fatalf("status = %q, want ok", status)
	}
	if strings.Contains(string(body), "</observation>") || strings.Contains(string(body), "<control>") {
		t.Fatalf("raw output was not escaped: %q", body)
	}
}

func TestFormatObservation(t *testing.T) {
	body := []byte(`{"output":"x"}`)
	got := formatObservation("read_file", json.RawMessage(`{"path":"notes.txt"}`), "ok", body)
	want := `<observation tool="read_file" path="notes.txt" status="ok">
{"output":"x"}
</observation>`
	if got != want {
		t.Fatalf("formatObservation() = %q, want %q", got, want)
	}
}

func TestFormatObservationWithoutPath(t *testing.T) {
	got := formatObservation("read_file", json.RawMessage(`{}`), "error", []byte(`{"error":"missing"}`))
	want := `<observation tool="read_file" status="error">
{"error":"missing"}
</observation>`
	if got != want {
		t.Fatalf("formatObservation() = %q, want %q", got, want)
	}
}

func TestFormatObservationEscapesAttributes(t *testing.T) {
	got := formatObservation(
		"a\"b\nc",
		json.RawMessage(`{"path":"p\"q\nr"}`),
		"ok",
		[]byte("{}"),
	)
	if !strings.Contains(got, `tool="a&#34;b&#10;c"`) {
		t.Fatalf("tool attribute was not escaped: %q", got)
	}
	if !strings.Contains(got, `path="p&#34;q&#10;r"`) {
		t.Fatalf("path attribute was not escaped: %q", got)
	}
}

func TestFormatObservationEmptyBody(t *testing.T) {
	got := formatObservation("read_file", nil, "ok", nil)
	if !strings.Contains(got, "\n{}\n") {
		t.Fatalf("formatObservation() = %q, want empty JSON object body", got)
	}
}

func TestActionInputPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "path", input: `{"path":"notes.txt"}`, want: "notes.txt", ok: true},
		{name: "missing", input: `{"other":"x"}`, ok: false},
		{name: "null", input: `{"path":null}`, ok: false},
		{name: "number", input: `{"path":1}`, ok: false},
		{name: "empty", input: `{"path":""}`, ok: false},
		{name: "invalid", input: "{", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := actionInputPath(json.RawMessage(tt.input))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("actionInputPath() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}
