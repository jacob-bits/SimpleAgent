// Package agent implements SimpleAgent's ReAct loop and step protocol.
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxStepBytes is the maximum accepted size of one model response.
	MaxStepBytes = 1 << 20 // 1 MiB

	// MaxErrorSnippetRunes bounds ParseError.Snippet.
	MaxErrorSnippetRunes = 512

	// MaxErrorDetailRunes bounds ParseError.Detail.
	MaxErrorDetailRunes = 1024

	maxUnknownFields  = 10
	maxFieldNameRunes = 64

	backtick = byte(96)
)

// fenceMarker is a Markdown fenced-code marker.
const fenceMarker = string(backtick) + string(backtick) + string(backtick)

// ErrInvalidOutput marks every failure to parse a ReAct step.
var ErrInvalidOutput = errors.New("invalid model response")

// Step is one parsed ReAct step.
//
// Exactly one of Action and FinalAnswer is non-empty. ActionInput is set only
// for an action step and always contains a JSON object.
type Step struct {
	Thought     string          `json:"thought"`
	Action      string          `json:"action,omitempty"`
	ActionInput json.RawMessage `json:"action_input,omitempty"`
	FinalAnswer string          `json:"final_answer,omitempty"`
}

// ParseErrorKind identifies the reason a model response was rejected.
type ParseErrorKind string

const (
	ParseErrEmpty           ParseErrorKind = "empty_response"
	ParseErrTooLarge        ParseErrorKind = "response_too_large"
	ParseErrInvalidUTF8     ParseErrorKind = "invalid_utf8"
	ParseErrNoObject        ParseErrorKind = "no_json_object"
	ParseErrInvalidJSON     ParseErrorKind = "invalid_json"
	ParseErrNotObject       ParseErrorKind = "not_object"
	ParseErrUnknownField    ParseErrorKind = "unknown_field"
	ParseErrDuplicateField  ParseErrorKind = "duplicate_field"
	ParseErrFieldType       ParseErrorKind = "field_type"
	ParseErrConflict        ParseErrorKind = "conflict"
	ParseErrMissingOutcome  ParseErrorKind = "missing_outcome"
	ParseErrEmptyAction     ParseErrorKind = "empty_action"
	ParseErrMissingInput    ParseErrorKind = "missing_action_input"
	ParseErrInvalidInput    ParseErrorKind = "invalid_action_input"
	ParseErrEmptyFinal      ParseErrorKind = "empty_final_answer"
	ParseErrUnexpectedInput ParseErrorKind = "unexpected_action_input"
)

// ParseError is returned for every invalid model response.
//
// Detail and Snippet are already bounded. Snippet is untrusted model output;
// callers should format it with %q or use Error(), which quotes it.
type ParseError struct {
	Kind    ParseErrorKind
	Detail  string
	Snippet string
}

// Error implements error.
func (e *ParseError) Error() string {
	if e == nil {
		return ErrInvalidOutput.Error()
	}

	var b strings.Builder
	b.WriteString(ErrInvalidOutput.Error())
	if e.Kind != "" {
		b.WriteString(": ")
		b.WriteString(string(e.Kind))
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Snippet != "" {
		b.WriteString(" (raw=")
		b.WriteString(fmt.Sprintf("%q", e.Snippet))
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap makes ParseError match ErrInvalidOutput with errors.Is.
func (e *ParseError) Unwrap() error {
	return ErrInvalidOutput
}

// ParseStep parses one ReAct step from a model response.
//
// It accepts a bare JSON object, a Markdown fenced JSON block, or a JSON
// object surrounded by a small amount of prose. It validates only protocol
// shape: tool existence and tool argument schemas are checked later by the
// Registry and Invoker.
func ParseStep(raw string) (Step, error) {
	if len(raw) > MaxStepBytes {
		return Step{}, newParseError(
			ParseErrTooLarge,
			fmt.Sprintf("response is %d bytes, limit is %d", len(raw), MaxStepBytes),
			raw,
		)
	}
	if !utf8.ValidString(raw) {
		return Step{}, newParseError(ParseErrInvalidUTF8, "response is not valid UTF-8", raw)
	}

	text := trimStepText(raw)
	if text == "" {
		return Step{}, newParseError(ParseErrEmpty, "response is empty", raw)
	}

	return parseSource(selectPayload(text))
}

func trimStepText(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "\ufeff")
	return strings.TrimSpace(value)
}

// parseSource parses the selected payload. source is also used as the error
// snippet, so a fenced block error points at the block, not at the whole
// response.
func parseSource(source string) (Step, error) {
	source = trimStepText(source)
	if source == "" {
		return Step{}, newParseError(ParseErrEmpty, "response is empty", source)
	}

	if json.Valid([]byte(source)) {
		if source[0] != '{' {
			return Step{}, newParseError(ParseErrNotObject, "top-level value must be a JSON object", source)
		}
		fields, err := decodeObject(source)
		if err != nil {
			return Step{}, decodeObjectError(err, source)
		}
		return validateStep(fields, source)
	}

	// A pure or truncated array must not yield an object from its interior.
	if source[0] == '[' {
		return Step{}, newParseError(ParseErrNotObject, "top-level value must be a JSON object", source)
	}

	return scanAndParse(source)
}

// scanAndParse finds the first syntactically valid JSON object in source.
//
// Malformed balanced fragments are skipped. The first object that decodes is
// authoritative: field validation errors stop the search. Duplicate keys are a
// protocol error and also stop the search immediately.
func scanAndParse(source string) (Step, error) {
	sawBrace := false
	var lastErr error

	for i := 0; i < len(source); {
		if source[i] != '{' {
			i++
			continue
		}
		sawBrace = true

		end, ok := matchObject(source, i)
		if !ok {
			break
		}

		fields, err := decodeObject(source[i:end])
		if err != nil {
			if errors.Is(err, errDuplicateKey) {
				return Step{}, newParseError(ParseErrDuplicateField, err.Error(), source)
			}
			lastErr = err
			i = end
			continue
		}
		return validateStep(fields, source)
	}

	if sawBrace {
		detail := "no valid JSON object found"
		if lastErr != nil {
			detail = "no valid JSON object: " + lastErr.Error()
		}
		return Step{}, newParseError(ParseErrInvalidJSON, detail, source)
	}
	return Step{}, newParseError(ParseErrNoObject, "no JSON object found", source)
}

// matchObject returns the end offset of the balanced object starting at start.
// Braces inside JSON strings do not affect nesting.
func matchObject(source string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(source); i++ {
		c := source[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return len(source), false
}

var (
	errNotObject    = errors.New("top-level value must be a JSON object")
	errDuplicateKey = errors.New("duplicate top-level key")
)

// decodeObject decodes one JSON object and preserves each field as raw JSON.
// It rejects duplicate top-level keys so the object cannot mean different
// things to the conflict check and the tool executor.
func decodeObject(source string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(source))

	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errNotObject
	}

	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("%w: %s", errDuplicateKey, quoteFieldName(key))
		}

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}

	token, err = dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return nil, errors.New("invalid object closing")
	}

	var trailing json.RawMessage
	switch err := dec.Decode(&trailing); {
	case err == nil:
		return nil, errors.New("unexpected data after top-level object")
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	return fields, nil
}

func decodeObjectError(err error, source string) *ParseError {
	switch {
	case errors.Is(err, errNotObject):
		return newParseError(ParseErrNotObject, "top-level value must be a JSON object", source)
	case errors.Is(err, errDuplicateKey):
		return newParseError(ParseErrDuplicateField, err.Error(), source)
	default:
		return newParseError(ParseErrInvalidJSON, "invalid JSON object: "+err.Error(), source)
	}
}

// validateStep checks field presence, types and mutual exclusion in a fixed
// order so callers and tests see deterministic errors.
func validateStep(fields map[string]json.RawMessage, source string) (Step, error) {
	unknown := make([]string, 0)
	for name := range fields {
		if !isKnownStepField(name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Step{}, newParseError(ParseErrUnknownField, unknownFieldsDetail(unknown), source)
	}

	_, hasAction := fields["action"]
	_, hasFinal := fields["final_answer"]
	if hasAction && hasFinal {
		return Step{}, newParseError(
			ParseErrConflict,
			`"action" and "final_answer" are mutually exclusive`,
			source,
		)
	}
	if !hasAction && !hasFinal {
		return Step{}, newParseError(
			ParseErrMissingOutcome,
			`exactly one of "action" or "final_answer" is required`,
			source,
		)
	}

	var step Step
	if raw, ok := fields["thought"]; ok {
		thought, err := decodeStringField("thought", raw)
		if err != nil {
			return Step{}, newParseError(ParseErrFieldType, err.Error(), source)
		}
		step.Thought = thought
	}

	if hasAction {
		action, err := decodeStringField("action", fields["action"])
		if err != nil {
			return Step{}, newParseError(ParseErrFieldType, err.Error(), source)
		}
		if strings.TrimSpace(action) == "" {
			return Step{}, newParseError(ParseErrEmptyAction, `"action" must not be empty`, source)
		}

		inputRaw, ok := fields["action_input"]
		if !ok {
			return Step{}, newParseError(
				ParseErrMissingInput,
				`"action_input" is required when "action" is set`,
				source,
			)
		}
		input, err := compactObjectField("action_input", inputRaw)
		if err != nil {
			return Step{}, newParseError(ParseErrInvalidInput, err.Error(), source)
		}

		step.Action = action
		step.ActionInput = input
		return step, nil
	}

	final, err := decodeStringField("final_answer", fields["final_answer"])
	if err != nil {
		return Step{}, newParseError(ParseErrFieldType, err.Error(), source)
	}
	if strings.TrimSpace(final) == "" {
		return Step{}, newParseError(ParseErrEmptyFinal, `"final_answer" must not be empty`, source)
	}
	if _, ok := fields["action_input"]; ok {
		return Step{}, newParseError(
			ParseErrUnexpectedInput,
			`"action_input" is not allowed with "final_answer"`,
			source,
		)
	}

	step.FinalAnswer = final
	return step, nil
}

func isKnownStepField(name string) bool {
	switch name {
	case "thought", "action", "action_input", "final_answer":
		return true
	default:
		return false
	}
}

func decodeStringField(name string, raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("field %q must be a string", name)
	}

	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", fmt.Errorf("field %q must be a string", name)
	}
	return value, nil
}

func compactObjectField(name string, raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("field %q must be a JSON object", name)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("field %q must be a JSON object", name)
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, fmt.Errorf("field %q must be a JSON object", name)
	}
	return json.RawMessage(append([]byte(nil), compact.Bytes()...)), nil
}

// selectPayload prefers an explicit fenced payload over prose objects.
func selectPayload(text string) string {
	blocks := scanFences(text)
	for _, block := range blocks {
		if strings.EqualFold(block.info, "json") {
			return block.body
		}
	}
	for _, block := range blocks {
		if block.info != "" {
			continue
		}
		body := trimStepText(block.body)
		if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
			return block.body
		}
	}
	return text
}

type fenceBlock struct {
	info string
	body string
}

// scanFences returns Markdown backtick fences. It supports up to three leading
// spaces before an opening or closing fence and ignores fences with other
// language tags when selecting the payload later.
func scanFences(text string) []fenceBlock {
	if !strings.Contains(text, fenceMarker) {
		return nil
	}

	lines := strings.Split(text, "\n")
	blocks := make([]fenceBlock, 0)
	for i := 0; i < len(lines); i++ {
		openCount, info, ok := parseFenceOpen(lines[i])
		if !ok {
			continue
		}

		bodyStart := i + 1
		closeIndex := -1
		for j := bodyStart; j < len(lines); j++ {
			if isFenceClose(lines[j], openCount) {
				closeIndex = j
				break
			}
		}

		if closeIndex < 0 {
			blocks = append(blocks, fenceBlock{
				info: info,
				body: strings.Join(lines[bodyStart:], "\n"),
			})
			break
		}
		blocks = append(blocks, fenceBlock{
			info: info,
			body: strings.Join(lines[bodyStart:closeIndex], "\n"),
		})
		i = closeIndex
	}
	return blocks
}

func parseFenceOpen(line string) (int, string, bool) {
	rest, ok := trimFenceIndent(line)
	if !ok {
		return 0, "", false
	}

	count := 0
	for count < len(rest) && rest[count] == backtick {
		count++
	}
	if count < 3 {
		return 0, "", false
	}

	info := strings.TrimSpace(rest[count:])
	if fields := strings.Fields(info); len(fields) > 0 {
		info = fields[0]
	}
	return count, info, true
}

func isFenceClose(line string, minCount int) bool {
	rest, ok := trimFenceIndent(line)
	if !ok {
		return false
	}

	count := 0
	for count < len(rest) && rest[count] == backtick {
		count++
	}
	if count < minCount {
		return false
	}
	return strings.TrimSpace(rest[count:]) == ""
}

func trimFenceIndent(line string) (string, bool) {
	line = strings.TrimSuffix(line, "\r")
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
		if i > 3 {
			return "", false
		}
	}
	return line[i:], true
}

func unknownFieldsDetail(names []string) string {
	total := len(names)
	shown := names
	if len(shown) > maxUnknownFields {
		shown = shown[:maxUnknownFields]
	}

	quoted := make([]string, len(shown))
	for i, name := range shown {
		quoted[i] = quoteFieldName(name)
	}

	var detail string
	if total == 1 {
		detail = "unknown field " + quoted[0]
	} else {
		detail = "unknown fields " + strings.Join(quoted, ", ")
	}
	if total > len(shown) {
		detail += fmt.Sprintf(" (%d more)", total-len(shown))
	}
	return detail
}

func quoteFieldName(name string) string {
	return fmt.Sprintf("%q", truncateRunes(name, maxFieldNameRunes))
}

func newParseError(kind ParseErrorKind, detail, source string) *ParseError {
	return &ParseError{
		Kind:    kind,
		Detail:  truncateRunes(detail, MaxErrorDetailRunes),
		Snippet: truncateRunes(source, MaxErrorSnippetRunes),
	}
}

// truncateRunes returns at most max runes. When it truncates, the ellipsis is
// included in max, so the result never exceeds the bound.
func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if max <= 3 {
		count := 0
		for i := range value {
			if count == max {
				return value[:i]
			}
			count++
		}
		return value
	}

	keep := max - 3
	count := 0
	cut := 0
	for i := range value {
		if count == keep {
			cut = i
		}
		if count == max {
			return value[:cut] + "..."
		}
		count++
	}
	return value
}
