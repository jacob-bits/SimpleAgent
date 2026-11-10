// Package tool defines the production tool contract: metadata, structured
// parameters, normalized results, registry, invocation policy and schema
// generation.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
)

const (
	// CurrencyCNY is the only supported currency for tool costs.
	CurrencyCNY = "CNY"
	// FenPerYuan is the number of fen in one yuan.
	FenPerYuan int64 = 100
)

// ToolCategory groups tools for policy and audit purposes.
type ToolCategory string

const (
	CategorySearch      ToolCategory = "search"
	CategoryCalculation ToolCategory = "calculation"
	CategoryFile        ToolCategory = "file"
	CategoryNetwork     ToolCategory = "network"
	CategorySystem      ToolCategory = "system"
)

// Valid reports whether c is one of the supported categories.
func (c ToolCategory) Valid() bool {
	switch c {
	case CategorySearch, CategoryCalculation, CategoryFile, CategoryNetwork, CategorySystem:
		return true
	default:
		return false
	}
}

// ToolMetadata describes a tool's identity and runtime policy.
type ToolMetadata struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Category    ToolCategory `json:"category"`

	RequiresAuth   bool `json:"requires_auth"`
	RateLimit      *int `json:"rate_limit,omitempty"` // calls per minute; nil = unlimited
	TimeoutSeconds int  `json:"timeout_seconds"`
	MemoryLimitMB  int  `json:"memory_limit_mb"`
	Sandboxed      bool `json:"sandboxed"`
	Dangerous      bool `json:"dangerous"`

	// CostPerUse is the cost of one invocation in fen (1 CNY = 100 fen).
	CostPerUse int64 `json:"cost_per_use"`
}

// DefaultToolMetadata returns the documented defaults. Name, version,
// description and category are intentionally left empty and must be set.
func DefaultToolMetadata() ToolMetadata {
	return ToolMetadata{
		TimeoutSeconds: 30,
		MemoryLimitMB:  512,
		Sandboxed:      true,
		CostPerUse:     0,
	}
}

// NewToolMetadata applies defaults and sets the required identity fields.
func NewToolMetadata(name, version, description string, category ToolCategory) ToolMetadata {
	m := DefaultToolMetadata()
	m.Name = name
	m.Version = version
	m.Description = description
	m.Category = category
	return m
}

// UnmarshalJSON applies defaults before decoding, so omitted fields keep
// their documented values, especially sandboxed=true.
func (m *ToolMetadata) UnmarshalJSON(data []byte) error {
	type alias ToolMetadata
	v := alias(DefaultToolMetadata())
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*m = ToolMetadata(v)
	return nil
}

var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Validate checks metadata constraints. It reports all problems at once.
func (m ToolMetadata) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !toolNameRE.MatchString(m.Name) {
		add("name must match %s, got %q", toolNameRE.String(), m.Name)
	}
	if strings.TrimSpace(m.Version) == "" {
		add("version is required")
	}
	if strings.TrimSpace(m.Description) == "" {
		add("description is required")
	}
	if !m.Category.Valid() {
		add("category %q is not supported", m.Category)
	}
	if m.RateLimit != nil && *m.RateLimit <= 0 {
		add("rate_limit must be > 0 or nil, got %d", *m.RateLimit)
	}
	if m.TimeoutSeconds <= 0 {
		add("timeout_seconds must be > 0, got %d", m.TimeoutSeconds)
	}
	if m.MemoryLimitMB <= 0 {
		add("memory_limit_mb must be > 0, got %d", m.MemoryLimitMB)
	}
	if m.CostPerUse < 0 {
		add("cost_per_use must be >= 0 fen, got %d", m.CostPerUse)
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid tool metadata: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ToolParameterType is the JSON type of a tool parameter.
type ToolParameterType string

const (
	ParameterString  ToolParameterType = "string"
	ParameterInteger ToolParameterType = "integer"
	ParameterFloat   ToolParameterType = "number" // FLOAT maps to JSON Schema number
	ParameterBoolean ToolParameterType = "boolean"
	ParameterArray   ToolParameterType = "array"
	ParameterObject  ToolParameterType = "object"
)

// Valid reports whether t is a supported parameter type.
func (t ToolParameterType) Valid() bool {
	switch t {
	case ParameterString, ParameterInteger, ParameterFloat, ParameterBoolean, ParameterArray, ParameterObject:
		return true
	default:
		return false
	}
}

func (t ToolParameterType) describe() string {
	switch t {
	case ParameterString:
		return "a string"
	case ParameterInteger:
		return "an integer"
	case ParameterFloat:
		return "a number"
	case ParameterBoolean:
		return "a boolean"
	case ParameterArray:
		return "an array"
	case ParameterObject:
		return "an object"
	default:
		return "a valid value"
	}
}

// ToolParameter describes one action_input field.
type ToolParameter struct {
	Name        string            `json:"name"`
	Type        ToolParameterType `json:"type"`
	Description string            `json:"description"`
	Required    bool              `json:"required"`
	Default     any               `json:"default,omitempty"`
	Enum        []any             `json:"enum,omitempty"`
	MinValue    *float64          `json:"min_value,omitempty"`
	MaxValue    *float64          `json:"max_value,omitempty"`
	Pattern     *string           `json:"pattern,omitempty"`
}

// NewToolParameter creates a required parameter. Tools with optional
// parameters must set Required=false explicitly.
func NewToolParameter(name string, typ ToolParameterType, description string) ToolParameter {
	return ToolParameter{
		Name:        name,
		Type:        typ,
		Description: description,
		Required:    true,
	}
}

// UnmarshalJSON applies the required=true default before decoding.
func (p *ToolParameter) UnmarshalJSON(data []byte) error {
	type alias ToolParameter
	v := alias{Required: true}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*p = ToolParameter(v)
	return nil
}

var parameterNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Validate checks parameter constraints, including enum and default values.
func (p ToolParameter) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !parameterNameRE.MatchString(p.Name) {
		add("name must match %s, got %q", parameterNameRE.String(), p.Name)
	}
	if !p.Type.Valid() {
		add("type %q is not supported", p.Type)
	}
	if strings.TrimSpace(p.Description) == "" {
		add("description is required")
	}
	if p.Required && p.Default != nil {
		add("default is only allowed when required=false")
	}

	if p.MinValue != nil || p.MaxValue != nil {
		if p.Type != ParameterInteger && p.Type != ParameterFloat {
			add("min_value/max_value are only allowed for numeric types")
		}
		if p.MinValue != nil && (math.IsNaN(*p.MinValue) || math.IsInf(*p.MinValue, 0)) {
			add("min_value must be a finite number")
		}
		if p.MaxValue != nil && (math.IsNaN(*p.MaxValue) || math.IsInf(*p.MaxValue, 0)) {
			add("max_value must be a finite number")
		}
		if p.MinValue != nil && p.MaxValue != nil && *p.MinValue > *p.MaxValue {
			add("min_value (%v) must be <= max_value (%v)", *p.MinValue, *p.MaxValue)
		}
		if p.Type == ParameterInteger {
			if p.MinValue != nil && math.Trunc(*p.MinValue) != *p.MinValue {
				add("min_value must be an integer for integer parameters")
			}
			if p.MaxValue != nil && math.Trunc(*p.MaxValue) != *p.MaxValue {
				add("max_value must be an integer for integer parameters")
			}
		}
	}

	if p.Pattern != nil {
		if p.Type != ParameterString {
			add("pattern is only allowed for string parameters")
		} else if _, err := regexp.Compile(*p.Pattern); err != nil {
			add("pattern is not a valid regular expression: %v", err)
		}
	}

	if len(p.Enum) > 0 {
		for i, value := range p.Enum {
			raw, err := json.Marshal(value)
			if err != nil {
				add("enum[%d] is not JSON-serializable: %v", i, err)
				continue
			}
			if p.Type.Valid() {
				if err := p.validateValue(raw); err != nil {
					add("enum[%d]: %v", i, err)
				}
			}
		}
	}

	if p.Default != nil {
		raw, err := json.Marshal(p.Default)
		if err != nil {
			add("default is not JSON-serializable: %v", err)
		} else if p.Type.Valid() {
			if err := p.validateValue(raw); err != nil {
				add("default: %v", err)
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid parameter %q: %s", p.Name, strings.Join(problems, "; "))
	}
	return nil
}

// validateValue checks one JSON value against the parameter definition.
// The returned error text is intended to be model-visible.
func (p ToolParameter) validateValue(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s must be valid JSON: %v", p.Name, err)
	}
	if value == nil {
		return fmt.Errorf("%s must not be null", p.Name)
	}
	if !valueMatchesType(value, p.Type) {
		return fmt.Errorf("%s must be %s", p.Name, p.Type.describe())
	}

	if len(p.Enum) > 0 && !valueInEnum(value, p.Enum) {
		return fmt.Errorf("%s must be one of %s", p.Name, formatValues(p.Enum))
	}

	if p.MinValue != nil || p.MaxValue != nil {
		n, ok := numericValue(value)
		if !ok {
			return fmt.Errorf("%s must be numeric", p.Name)
		}
		if p.MinValue != nil && n < *p.MinValue {
			return fmt.Errorf("%s must be >= %v", p.Name, *p.MinValue)
		}
		if p.MaxValue != nil && n > *p.MaxValue {
			return fmt.Errorf("%s must be <= %v", p.Name, *p.MaxValue)
		}
	}

	if p.Pattern != nil {
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", p.Name)
		}
		matched, err := regexp.MatchString(*p.Pattern, s)
		if err != nil {
			return fmt.Errorf("%s has an invalid pattern: %v", p.Name, err)
		}
		if !matched {
			return fmt.Errorf("%s must match pattern %q", p.Name, *p.Pattern)
		}
	}

	return nil
}

func valueMatchesType(value any, typ ToolParameterType) bool {
	switch typ {
	case ParameterString:
		_, ok := value.(string)
		return ok
	case ParameterInteger:
		f, ok := value.(float64)
		return ok && !math.IsNaN(f) && !math.IsInf(f, 0) &&
			math.Trunc(f) == f && f >= math.MinInt64 && f <= math.MaxInt64
	case ParameterFloat:
		f, ok := value.(float64)
		return ok && !math.IsNaN(f) && !math.IsInf(f, 0)
	case ParameterBoolean:
		_, ok := value.(bool)
		return ok
	case ParameterArray:
		_, ok := value.([]any)
		return ok
	case ParameterObject:
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func numericValue(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func valueInEnum(value any, enum []any) bool {
	normalized := normalizeJSONValue(value)
	for _, candidate := range enum {
		if reflect.DeepEqual(normalized, normalizeJSONValue(candidate)) {
			return true
		}
	}
	return false
}

func normalizeJSONValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return value
	}
	return normalized
}

func formatValues(values []any) string {
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Sprint(values)
	}
	return string(raw)
}

// ToolDefinition combines the metadata and parameter contract of a tool.
type ToolDefinition struct {
	Metadata   ToolMetadata    `json:"metadata"`
	Parameters []ToolParameter `json:"parameters"`
}

// Validate checks metadata, parameter definitions and schema generation.
func (d ToolDefinition) Validate() error {
	var problems []string

	if err := d.Metadata.Validate(); err != nil {
		problems = append(problems, err.Error())
	}

	seen := make(map[string]struct{}, len(d.Parameters))
	for i, p := range d.Parameters {
		if _, exists := seen[p.Name]; exists {
			problems = append(problems, fmt.Sprintf("parameter[%d] %q is duplicated", i, p.Name))
		}
		seen[p.Name] = struct{}{}
		if err := p.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("parameter[%d]: %v", i, err))
		}
	}

	if _, err := d.InputSchema(); err != nil {
		problems = append(problems, fmt.Sprintf("input schema: %v", err))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid tool definition %q: %s", d.Metadata.Name, strings.Join(problems, "; "))
	}
	return nil
}

// InputSchema generates the JSON Schema used in the system prompt.
func (d ToolDefinition) InputSchema() (json.RawMessage, error) {
	properties := make(map[string]any, len(d.Parameters))
	required := make([]string, 0, len(d.Parameters))

	for _, p := range d.Parameters {
		property := map[string]any{
			"type":        string(p.Type),
			"description": p.Description,
		}
		if len(p.Enum) > 0 {
			property["enum"] = p.Enum
		}
		if p.MinValue != nil {
			property["minimum"] = *p.MinValue
		}
		if p.MaxValue != nil {
			property["maximum"] = *p.MaxValue
		}
		if p.Pattern != nil {
			property["pattern"] = *p.Pattern
		}
		if p.Default != nil {
			property["default"] = p.Default
		}
		properties[p.Name] = property
		if p.Required {
			required = append(required, p.Name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return json.Marshal(schema)
}

// ToolResult is the normalized result of one tool invocation.
type ToolResult struct {
	Success         bool           `json:"success"`
	Output          any            `json:"output,omitempty"`
	Error           string         `json:"error,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	ExecutionTimeMS *int64         `json:"execution_time_ms,omitempty"`
	TokensUsed      *int           `json:"tokens_used,omitempty"`
}

// Validate checks the result invariant expected by the Agent.
func (r ToolResult) Validate() error {
	if r.Success {
		if r.Error != "" {
			return errors.New("successful result must not contain an error")
		}
	} else if r.Error == "" {
		return errors.New("failed result must contain an error")
	}

	if r.ExecutionTimeMS != nil && *r.ExecutionTimeMS < 0 {
		return fmt.Errorf("execution_time_ms must be >= 0, got %d", *r.ExecutionTimeMS)
	}
	if r.TokensUsed != nil && *r.TokensUsed < 0 {
		return fmt.Errorf("tokens_used must be >= 0, got %d", *r.TokensUsed)
	}
	if _, err := json.Marshal(r.Output); err != nil {
		return fmt.Errorf("output is not JSON-serializable: %v", err)
	}
	if _, err := json.Marshal(r.Metadata); err != nil {
		return fmt.Errorf("metadata is not JSON-serializable: %v", err)
	}
	return nil
}

// Tool is implemented by every callable tool.
type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// Ptr returns a pointer to v. It is useful for optional metadata fields.
func Ptr[T any](v T) *T {
	return &v
}
