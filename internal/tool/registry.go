package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Capabilities describes what the runtime can actually enforce. Tools must not
// be registered with policy requirements that the runtime cannot honor.
type Capabilities struct {
	Authenticator bool
	SandboxRunner bool
	Approver      bool
}

// RegistryOptions configures a MapRegistry.
type RegistryOptions struct {
	Capabilities Capabilities
	Invoker      *Invoker
}

// Registry is the read-only interface consumed by the Agent and prompt builder.
type Registry interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error)
}

// MapRegistry is a concurrency-safe in-memory tool registry.
type MapRegistry struct {
	mu      sync.RWMutex
	order   []string
	defs    map[string]ToolDefinition
	tools   map[string]Tool
	schemas map[string]json.RawMessage
	invoker *Invoker
}

// NewRegistry registers tools with no optional runtime capabilities.
func NewRegistry(tools ...Tool) (*MapRegistry, error) {
	return NewRegistryWithOptions(RegistryOptions{}, tools...)
}

// NewRegistryWithOptions registers tools and verifies that declared policy
// requirements are backed by the supplied runtime capabilities.
func NewRegistryWithOptions(opts RegistryOptions, tools ...Tool) (*MapRegistry, error) {
	r := &MapRegistry{
		defs:    make(map[string]ToolDefinition),
		tools:   make(map[string]Tool),
		schemas: make(map[string]json.RawMessage),
		invoker: opts.Invoker,
	}
	if r.invoker == nil {
		r.invoker = NewInvoker()
	}

	for _, t := range tools {
		if err := r.register(t, opts.Capabilities); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *MapRegistry) register(t Tool, caps Capabilities) error {
	if t == nil {
		return errors.New("tool is nil")
	}
	def := cloneDefinition(t.Definition())
	if err := def.Validate(); err != nil {
		return err
	}
	if err := checkCapabilities(def.Metadata, caps); err != nil {
		return err
	}

	name := def.Metadata.Name
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("duplicate tool %q", name)
	}
	schema, err := def.InputSchema()
	if err != nil {
		return fmt.Errorf("tool %q: generate input schema: %w", name, err)
	}

	r.order = append(r.order, name)
	r.defs[name] = def
	r.tools[name] = t
	r.schemas[name] = schema
	return nil
}

func checkCapabilities(m ToolMetadata, caps Capabilities) error {
	if m.RequiresAuth && !caps.Authenticator {
		return fmt.Errorf("tool %q requires authentication but no authenticator is configured", m.Name)
	}
	if m.Sandboxed && !caps.SandboxRunner {
		return fmt.Errorf("tool %q requires a sandbox but no sandbox runner is configured", m.Name)
	}
	if m.Dangerous && !caps.Approver {
		return fmt.Errorf("tool %q requires approval but no approver is configured", m.Name)
	}
	return nil
}

// cloneDefinition protects registry state from callers mutating returned
// definitions. Nested parameter values are still treated as read-only.
func cloneDefinition(def ToolDefinition) ToolDefinition {
	def.Parameters = append([]ToolParameter(nil), def.Parameters...)
	if def.Metadata.RateLimit != nil {
		rate := *def.Metadata.RateLimit
		def.Metadata.RateLimit = &rate
	}
	return def
}

// Definitions returns registered definitions in registration order.
// The returned slice is a copy; definitions are treated as immutable.
func (r *MapRegistry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, cloneDefinition(r.defs[name]))
	}
	return out
}

// InputSchema returns a copy of the schema generated at registration time.
func (r *MapRegistry) InputSchema(name string) (json.RawMessage, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	schema, ok := r.schemas[name]
	if !ok {
		return nil, false
	}
	return append(json.RawMessage(nil), schema...), true
}

// Execute runs a registered tool through the Invoker.
func (r *MapRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	r.mu.RLock()
	t, exists := r.tools[name]
	def := r.defs[name]
	r.mu.RUnlock()

	if !exists {
		return ToolResult{Success: false, Error: fmt.Sprintf("unknown tool: %s", name)}, nil
	}
	return r.invoker.Invoke(ctx, t, def, input)
}
