package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

// Invoker applies parameter validation, defaults, rate limits, budget checks,
// timeouts and result normalization around a Tool.
type Invoker struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
	budget  *Budget
	now     func() time.Time
}

// InvokerOption customizes an Invoker.
type InvokerOption func(*Invoker)

// WithBudget enables cost accounting for invocations.
func WithBudget(b *Budget) InvokerOption {
	return func(i *Invoker) {
		i.budget = b
	}
}

// WithClock injects a clock, primarily for tests.
func WithClock(now func() time.Time) InvokerOption {
	return func(i *Invoker) {
		if now != nil {
			i.now = now
		}
	}
}

// NewInvoker creates an Invoker.
func NewInvoker(opts ...InvokerOption) *Invoker {
	i := &Invoker{
		windows: make(map[string]*rateWindow),
		now:     time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(i)
		}
	}
	if i.now == nil {
		i.now = time.Now
	}
	return i
}

// Invoke validates and executes one tool call.
//
// Model-visible failures (invalid arguments, rate limit, budget) are returned
// as a failed ToolResult with a nil Go error. Context cancellation and
// internal contract violations are returned as Go errors.
func (i *Invoker) Invoke(ctx context.Context, t Tool, def ToolDefinition, input json.RawMessage) (ToolResult, error) {
	if t == nil {
		return ToolResult{}, errors.New("tool is nil")
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}

	normalized, err := normalizeInput(def, input)
	if err != nil {
		return ToolResult{Success: false, Error: "invalid arguments: " + err.Error()}, nil
	}

	if def.Metadata.RateLimit != nil && !i.allowRate(def.Metadata.Name, *def.Metadata.RateLimit) {
		return ToolResult{Success: false, Error: "rate limit exceeded"}, nil
	}

	if i.budget != nil {
		if err := i.budget.Charge(def.Metadata.CostPerUse); err != nil {
			return ToolResult{Success: false, Error: err.Error()}, nil
		}
	}

	runCtx := ctx
	cancel := func() {}
	if def.Metadata.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(def.Metadata.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	start := i.currentTime()
	result, err := t.Execute(runCtx, normalized)
	elapsed := i.currentTime().Sub(start)
	if err != nil {
		return ToolResult{}, err
	}

	ms := elapsed.Milliseconds()
	result.ExecutionTimeMS = &ms
	if err := result.Validate(); err != nil {
		return ToolResult{}, fmt.Errorf("tool %q returned invalid result: %w", def.Metadata.Name, err)
	}
	return result, nil
}

type rateWindow struct {
	start time.Time
	count int
}

func (i *Invoker) allowRate(name string, limit int) bool {
	if limit <= 0 {
		return false
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.windows == nil {
		i.windows = make(map[string]*rateWindow)
	}

	now := i.currentTime()
	window := i.windows[name]
	if window == nil || now.Sub(window.start) >= time.Minute || now.Before(window.start) {
		i.windows[name] = &rateWindow{start: now, count: 1}
		return true
	}
	if window.count >= limit {
		return false
	}
	window.count++
	return true
}

func (i *Invoker) currentTime() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

// normalizeInput validates action_input against the definition, applies
// defaults, rejects unknown fields and returns canonical JSON for the tool.
func normalizeInput(def ToolDefinition, input json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("action_input must be a JSON object")
	}

	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode action_input: %v", err)
	}
	var trailing json.RawMessage
	switch err := dec.Decode(&trailing); {
	case err == nil:
		return nil, errors.New("action_input must contain exactly one JSON object")
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("decode action_input: %v", err)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	params := make(map[string]ToolParameter, len(def.Parameters))
	for _, p := range def.Parameters {
		params[p.Name] = p
	}
	for name := range raw {
		if _, ok := params[name]; !ok {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
	}

	for _, p := range def.Parameters {
		value, present := raw[p.Name]
		if !present {
			if p.Required {
				return nil, fmt.Errorf("missing required parameter %q", p.Name)
			}
			if p.Default != nil {
				encoded, err := json.Marshal(p.Default)
				if err != nil {
					return nil, fmt.Errorf("default for parameter %q is not JSON-serializable: %v", p.Name, err)
				}
				raw[p.Name] = encoded
			}
			continue
		}
		if err := p.validateValue(value); err != nil {
			return nil, err
		}
	}

	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode normalized action_input: %v", err)
	}
	return normalized, nil
}

// Budget enforces a total cost limit in fen (CNY).
//
// A negative limit means unlimited; cost is still accumulated for accounting.
// The zero value is not usable; construct with NewBudget.
type Budget struct {
	mu        sync.Mutex
	limitFen  int64
	spentFen  int64
	unlimited bool
}

// NewBudget creates a budget with the given limit in fen. A negative limit
// means unlimited.
func NewBudget(limitFen int64) *Budget {
	return &Budget{
		limitFen:  limitFen,
		unlimited: limitFen < 0,
	}
}

// Charge reserves costFen from the budget.
func (b *Budget) Charge(costFen int64) error {
	if costFen < 0 {
		return errors.New("cost must be >= 0")
	}
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spentFen > math.MaxInt64-costFen {
		return errors.New("cost overflow")
	}
	if !b.unlimited && costFen > b.limitFen-b.spentFen {
		return errors.New("cost budget exceeded")
	}
	b.spentFen += costFen
	return nil
}

// SpentFen returns the total cost charged so far.
func (b *Budget) SpentFen() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spentFen
}

// LimitFen returns the configured limit; negative means unlimited.
func (b *Budget) LimitFen() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limitFen
}
