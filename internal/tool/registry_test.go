package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"simpleagent/internal/tool"
)

func TestNewRegistry(t *testing.T) {
	t.Run("valid tool", func(t *testing.T) {
		ft := &fakeTool{def: baseDefinition("demo")}
		registry, err := tool.NewRegistry(ft)
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		defs := registry.Definitions()
		if len(defs) != 1 || defs[0].Metadata.Name != "demo" {
			t.Fatalf("Definitions() = %+v", defs)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		a := &fakeTool{def: baseDefinition("demo")}
		b := &fakeTool{def: baseDefinition("demo")}
		_, err := tool.NewRegistry(a, b)
		if err == nil || !strings.Contains(err.Error(), "duplicate tool") {
			t.Fatalf("error = %v, want duplicate tool", err)
		}
	})

	t.Run("invalid definition", func(t *testing.T) {
		ft := &fakeTool{def: baseDefinition("Bad")}
		_, err := tool.NewRegistry(ft)
		if err == nil || !strings.Contains(err.Error(), "name must match") {
			t.Fatalf("error = %v, want invalid name", err)
		}
	})

	t.Run("nil tool", func(t *testing.T) {
		_, err := tool.NewRegistry(nil)
		if err == nil || !strings.Contains(err.Error(), "tool is nil") {
			t.Fatalf("error = %v, want nil tool", err)
		}
	})

	t.Run("auth capability missing", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.RequiresAuth = true
		_, err := tool.NewRegistry(&fakeTool{def: def})
		if err == nil || !strings.Contains(err.Error(), "requires authentication") {
			t.Fatalf("error = %v, want auth capability error", err)
		}
	})

	t.Run("auth capability present", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.RequiresAuth = true
		_, err := tool.NewRegistryWithOptions(tool.RegistryOptions{
			Capabilities: tool.Capabilities{Authenticator: true},
		}, &fakeTool{def: def})
		if err != nil {
			t.Fatalf("NewRegistryWithOptions: %v", err)
		}
	})

	t.Run("sandbox capability missing", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.Sandboxed = true
		_, err := tool.NewRegistry(&fakeTool{def: def})
		if err == nil || !strings.Contains(err.Error(), "requires a sandbox") {
			t.Fatalf("error = %v, want sandbox capability error", err)
		}
	})

	t.Run("sandbox capability present", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.Sandboxed = true
		_, err := tool.NewRegistryWithOptions(tool.RegistryOptions{
			Capabilities: tool.Capabilities{SandboxRunner: true},
		}, &fakeTool{def: def})
		if err != nil {
			t.Fatalf("NewRegistryWithOptions: %v", err)
		}
	})

	t.Run("approval capability missing", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.Dangerous = true
		_, err := tool.NewRegistry(&fakeTool{def: def})
		if err == nil || !strings.Contains(err.Error(), "requires approval") {
			t.Fatalf("error = %v, want approval capability error", err)
		}
	})

	t.Run("approval capability present", func(t *testing.T) {
		def := baseDefinition("demo")
		def.Metadata.Dangerous = true
		_, err := tool.NewRegistryWithOptions(tool.RegistryOptions{
			Capabilities: tool.Capabilities{Approver: true},
		}, &fakeTool{def: def})
		if err != nil {
			t.Fatalf("NewRegistryWithOptions: %v", err)
		}
	})
}

func TestRegistryDefinitionsOrderAndCopy(t *testing.T) {
	a := &fakeTool{def: baseDefinition("a_tool")}
	b := &fakeTool{def: baseDefinition("b_tool")}
	registry, err := tool.NewRegistry(a, b)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 2 || defs[0].Metadata.Name != "a_tool" || defs[1].Metadata.Name != "b_tool" {
		t.Fatalf("Definitions() = %+v", defs)
	}

	defs[0].Metadata.Name = "changed"
	defs[0].Parameters[0].Name = "changed"
	fresh := registry.Definitions()[0]
	if fresh.Metadata.Name != "a_tool" {
		t.Fatalf("registry metadata mutated through returned slice: %q", fresh.Metadata.Name)
	}
	if fresh.Parameters[0].Name != "path" {
		t.Fatalf("registry parameters mutated through returned slice: %q", fresh.Parameters[0].Name)
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	registry, err := tool.NewRegistry(&fakeTool{def: baseDefinition("demo")})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "missing", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if result.Success {
		t.Fatal("Success = true, want false")
	}
	if !strings.Contains(result.Error, "unknown tool") {
		t.Fatalf("Error = %q, want unknown tool", result.Error)
	}
}

func TestRegistryExecute(t *testing.T) {
	var captured json.RawMessage
	ft := &fakeTool{
		def: baseDefinition("demo"),
		exec: func(_ context.Context, input json.RawMessage) (tool.ToolResult, error) {
			captured = append(json.RawMessage(nil), input...)
			return tool.ToolResult{Success: true, Output: "done"}, nil
		},
	}
	registry, err := tool.NewRegistry(ft)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "demo", json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !result.Success || result.Output != "done" {
		t.Fatalf("result = %+v", result)
	}
	if result.ExecutionTimeMS == nil {
		t.Fatal("ExecutionTimeMS = nil, want filled by Invoker")
	}
	jsonEqual(t, captured, `{"path":"x"}`)
}

func TestRegistryInputSchema(t *testing.T) {
	registry, err := tool.NewRegistry(&fakeTool{def: baseDefinition("demo")})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	schema, ok := registry.InputSchema("demo")
	if !ok {
		t.Fatal("InputSchema(demo) not found")
	}
	jsonEqual(t, schema, `{
		"type":"object",
		"properties":{"path":{"type":"string","description":"path to read"}},
		"required":["path"],
		"additionalProperties":false
	}`)

	schema[0] = 'x'
	schema, ok = registry.InputSchema("demo")
	if !ok {
		t.Fatal("InputSchema(demo) missing after returned copy mutation")
	}
	jsonEqual(t, schema, `{
		"type":"object",
		"properties":{"path":{"type":"string","description":"path to read"}},
		"required":["path"],
		"additionalProperties":false
	}`)

	if _, ok := registry.InputSchema("missing"); ok {
		t.Fatal("InputSchema(missing) = true, want false")
	}
}

func TestRegistryConcurrentExecute(t *testing.T) {
	ft := &fakeTool{def: baseDefinition("demo")}
	registry, err := tool.NewRegistry(ft)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	const calls = 32
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := registry.Execute(context.Background(), "demo", json.RawMessage(`{"path":"x"}`))
			if err != nil {
				errs <- err
				return
			}
			if !result.Success {
				errs <- &resultError{result.Error}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

type resultError struct{ message string }

func (e *resultError) Error() string { return e.message }
