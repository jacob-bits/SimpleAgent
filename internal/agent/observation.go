package agent

import (
	"encoding/json"
	"html"
	"strings"

	"simpleagent/internal/tool"
)

const (
	maxObservationToolRunes = 128
	maxObservationPathRunes = 256
)

type observationPayload struct {
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// renderToolResult converts a ToolResult into a status and a JSON observation
// body. The body is always a JSON object: success carries output, failure
// carries error.
func renderToolResult(res tool.ToolResult) (status string, body []byte) {
	if !res.Success {
		message := res.Error
		if message == "" {
			message = "tool reported failure without an error"
		}
		body, _ = json.Marshal(observationPayload{Error: message})
		return "error", body
	}

	body, err := json.Marshal(observationPayload{Output: res.Output})
	if err != nil {
		body, _ = json.Marshal(observationPayload{
			Error: "agent: tool output is not JSON-serializable",
		})
		return "error", body
	}
	return "ok", body
}

func formatObservation(action string, input json.RawMessage, status string, body []byte) string {
	if len(body) == 0 {
		body = []byte("{}")
	}

	var observation strings.Builder
	observation.WriteString("<observation tool=\"")
	observation.WriteString(escapeObservationAttr(truncateRunes(action, maxObservationToolRunes)))
	observation.WriteString("\"")

	if path, ok := actionInputPath(input); ok {
		observation.WriteString(" path=\"")
		observation.WriteString(escapeObservationAttr(truncateRunes(path, maxObservationPathRunes)))
		observation.WriteString("\"")
	}

	observation.WriteString(" status=\"")
	observation.WriteString(status)
	observation.WriteString("\">\n")
	observation.Write(body)
	observation.WriteString("\n</observation>")
	return observation.String()
}

func escapeObservationAttr(value string) string {
	value = html.EscapeString(value)
	value = strings.ReplaceAll(value, "\n", "&#10;")
	value = strings.ReplaceAll(value, "\r", "&#13;")
	value = strings.ReplaceAll(value, "\t", "&#9;")
	return value
}

func actionInputPath(input json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", false
	}
	raw, ok := fields["path"]
	if !ok {
		return "", false
	}

	var path string
	if err := json.Unmarshal(raw, &path); err != nil || path == "" {
		return "", false
	}
	return path, true
}
