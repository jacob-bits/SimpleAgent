package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"simpleagent/internal/config"
	"simpleagent/internal/llm"
	"simpleagent/internal/tool"
)

// StopReason describes why an Agent run ended.
type StopReason string

const (
	StopFinalAnswer   StopReason = "final_answer"
	StopMaxSteps      StopReason = "max_steps"
	StopRepeated      StopReason = "repeated_action"
	StopNoProgress    StopReason = "no_progress"
	StopHistoryBudget StopReason = "context_budget"
	StopTimeout       StopReason = "timeout"
	StopCanceled      StopReason = "canceled"
	StopInvalidOutput StopReason = "invalid_response"
	StopLLMError      StopReason = "llm_error"
)

// Result is the terminal result of one Agent.Run call.
type Result struct {
	Answer string
	Steps  int
	Reason StopReason
}

// Completer is the minimal LLM dependency of the Agent loop.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

// Agent executes a bounded ReAct loop over a tool registry.
type Agent struct {
	cfg          config.AgentConfig
	llm          Completer
	tools        tool.Registry
	systemPrompt string
	trace        TraceFunc
}

// Option customizes an Agent.
type Option func(*Agent)

// ErrEmptyQuestion is returned before a run starts when the question is blank.
var ErrEmptyQuestion = errors.New("agent: question is empty")

// ErrMaxSteps is returned when the loop reaches its step limit.
var ErrMaxSteps = errors.New("max steps reached")

// ErrRepeatedAction is returned when the same action exceeds its repeat limit.
var ErrRepeatedAction = errors.New("repeated action limit reached")

// ErrNoProgress is returned when consecutive observations are too similar.
var ErrNoProgress = errors.New("no observable progress")

// ErrContextBudget is returned when the history byte budget is exceeded.
var ErrContextBudget = errors.New("context budget exceeded")

// ErrTimeout is returned when the run deadline or an upstream deadline expires.
var ErrTimeout = errors.New("agent timeout")

// ErrCanceled is returned when the parent context is canceled.
var ErrCanceled = errors.New("agent canceled")

// New validates its inputs and builds an Agent. It performs no network or tool
// execution; the system prompt is built once from the registry definitions.
func New(
	cfg config.AgentConfig,
	completer Completer,
	registry tool.Registry,
	opts ...Option,
) (*Agent, error) {
	if err := validateAgentConfig(cfg); err != nil {
		return nil, err
	}
	if completer == nil {
		return nil, errors.New("agent: completer is nil")
	}
	if registry == nil {
		return nil, errors.New("agent: registry is nil")
	}

	systemPrompt, err := buildSystemPrompt(registry.Definitions())
	if err != nil {
		return nil, fmt.Errorf("%w: build system prompt: %v", config.ErrInvalidConfig, err)
	}

	a := &Agent{
		cfg:          cfg,
		llm:          completer,
		tools:        registry,
		systemPrompt: systemPrompt,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a, nil
}

func validateAgentConfig(cfg config.AgentConfig) error {
	problems := make([]string, 0)
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if cfg.MinSteps < 1 {
		add("min_steps must be >= 1, got %d", cfg.MinSteps)
	}
	if cfg.MaxSteps < 1 {
		add("max_steps must be >= 1, got %d", cfg.MaxSteps)
	}
	if cfg.MinSteps >= 1 && cfg.MaxSteps >= 1 && cfg.MinSteps > cfg.MaxSteps {
		add("min_steps (%d) must be <= max_steps (%d)", cfg.MinSteps, cfg.MaxSteps)
	}
	if cfg.MaxRepeatedActions < 1 {
		add("max_repeated_actions must be >= 1, got %d", cfg.MaxRepeatedActions)
	}
	if t := cfg.NoProgressThreshold; !(t >= 0 && t <= 1) {
		add("no_progress_threshold must be within [0,1], got %v", cfg.NoProgressThreshold)
	}
	if cfg.NoProgressMinChars < 0 {
		add("no_progress_min_chars must be >= 0, got %d", cfg.NoProgressMinChars)
	}
	if cfg.MaxHistoryChars <= 0 {
		add("max_history_chars must be > 0, got %d", cfg.MaxHistoryChars)
	}
	if cfg.TimeoutSeconds <= 0 {
		add("timeout_seconds must be > 0, got %d", cfg.TimeoutSeconds)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: invalid agent config: %s", config.ErrInvalidConfig, strings.Join(problems, "; "))
}

// Run executes one question through the ReAct loop.
func (a *Agent) Run(parent context.Context, question string) (Result, error) {
	if strings.TrimSpace(question) == "" {
		return Result{}, ErrEmptyQuestion
	}
	if parent == nil {
		return Result{}, errors.New("agent: nil context")
	}

	runCtx, cancel := context.WithTimeout(
		parent,
		time.Duration(a.cfg.TimeoutSeconds)*time.Second,
	)
	defer cancel()

	messages := []llm.Message{
		{Role: "system", Content: a.systemPrompt},
		{Role: "user", Content: question},
	}

	var lastSig string
	repeats := 0
	var prevObservation string
	hasPrevObservation := false

	for step := 1; step <= a.cfg.MaxSteps; step++ {
		if reason, err := contextStop(parent, runCtx); err != nil {
			return a.finish(Result{Steps: step, Reason: reason}, err)
		}
		if n := historyBytes(messages); n > a.cfg.MaxHistoryChars {
			err := fmt.Errorf("%w: history has %d bytes, limit %d",
				ErrContextBudget, n, a.cfg.MaxHistoryChars)
			return a.finish(Result{Steps: step, Reason: StopHistoryBudget}, err)
		}

		raw, err := a.llm.Complete(runCtx, messages)
		if err != nil {
			if reason, ctxErr := contextStop(parent, runCtx); ctxErr != nil {
				return a.finish(Result{Steps: step, Reason: reason}, ctxErr)
			}
			return a.finish(
				Result{Steps: step, Reason: StopLLMError},
				fmt.Errorf("step %d: %w", step, err),
			)
		}
		if reason, err := contextStop(parent, runCtx); err != nil {
			return a.finish(Result{Steps: step, Reason: reason}, err)
		}

		st, err := ParseStep(raw)
		if err != nil {
			return a.finish(
				Result{Steps: step, Reason: StopInvalidOutput},
				fmt.Errorf("step %d: %w", step, err),
			)
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: raw})
		a.traceStep(step, st)

		if st.FinalAnswer != "" {
			if step >= a.cfg.MinSteps {
				return a.finish(
					Result{Answer: st.FinalAnswer, Steps: step, Reason: StopFinalAnswer},
					nil,
				)
			}
			control := minStepsControl(a.cfg.MinSteps, step)
			messages = append(messages, llm.Message{Role: "user", Content: control})
			a.traceControl(step, control)
			continue
		}

		// The final allowed step may only finish the task. Do not execute a
		// tool that can no longer be observed by another LLM decision.
		if step == a.cfg.MaxSteps {
			err := fmt.Errorf("%w: step %d is still an action", ErrMaxSteps, step)
			return a.finish(Result{Steps: step, Reason: StopMaxSteps}, err)
		}

		sig, err := canonicalSignature(st.Action, st.ActionInput)
		if err != nil {
			return a.finish(
				Result{Steps: step, Reason: StopInvalidOutput},
				fmt.Errorf("%w: step %d: %v", ErrInvalidOutput, step, err),
			)
		}
		if sig == lastSig {
			repeats++
		} else {
			lastSig = sig
			repeats = 1
		}
		if repeats > a.cfg.MaxRepeatedActions {
			err := fmt.Errorf("%w: action %q repeated %d times (limit %d)",
				ErrRepeatedAction, st.Action, repeats, a.cfg.MaxRepeatedActions)
			return a.finish(Result{Steps: step, Reason: StopRepeated}, err)
		}

		if reason, err := contextStop(parent, runCtx); err != nil {
			return a.finish(Result{Steps: step, Reason: reason}, err)
		}

		res, toolErr := a.tools.Execute(runCtx, st.Action, st.ActionInput)
		if toolErr != nil {
			if reason, ctxErr := contextStop(parent, runCtx); ctxErr != nil {
				return a.finish(Result{Steps: step, Reason: reason}, ctxErr)
			}
			res = tool.ToolResult{Success: false, Error: toolErr.Error()}
		} else if reason, err := contextStop(parent, runCtx); err != nil {
			return a.finish(Result{Steps: step, Reason: reason}, err)
		}

		status, body := renderToolResult(res)
		a.traceTool(step, st.Action, status, string(body))

		bodyText := string(body)
		if hasPrevObservation {
			if isNoProgress(prevObservation, bodyText, a.cfg.NoProgressThreshold, a.cfg.NoProgressMinChars) {
				return a.finish(
					Result{Steps: step, Reason: StopNoProgress},
					fmt.Errorf("%w: previous and current observation are too similar", ErrNoProgress),
				)
			}
		}
		prevObservation, hasPrevObservation = bodyText, true

		messages = append(messages, llm.Message{
			Role:    "user",
			Content: formatObservation(st.Action, st.ActionInput, status, body),
		})
	}

	err := fmt.Errorf("%w: %d", ErrMaxSteps, a.cfg.MaxSteps)
	return a.finish(Result{Steps: a.cfg.MaxSteps, Reason: StopMaxSteps}, err)
}

func (a *Agent) finish(result Result, err error) (Result, error) {
	a.emit(TraceEvent{
		Kind:       TraceStop,
		Step:       result.Steps,
		StopReason: result.Reason,
		Err:        err,
	})
	return result, err
}

func (a *Agent) emit(event TraceEvent) {
	if a.trace != nil {
		a.trace(event)
	}
}

func (a *Agent) traceStep(step int, st Step) {
	a.emit(TraceEvent{
		Kind:        TraceStep,
		Step:        step,
		Thought:     truncateRunes(st.Thought, traceThoughtRunes),
		Action:      st.Action,
		ActionInput: truncateRunes(string(st.ActionInput), traceInputRunes),
		Final:       st.FinalAnswer != "",
	})
}

func (a *Agent) traceTool(step int, action, status, body string) {
	a.emit(TraceEvent{
		Kind:        TraceTool,
		Step:        step,
		Action:      action,
		Status:      status,
		Observation: truncateRunes(normalizeObservation(body), traceObservationRunes),
	})
}

func (a *Agent) traceControl(step int, control string) {
	a.emit(TraceEvent{
		Kind:    TraceControl,
		Step:    step,
		Control: truncateRunes(control, traceControlRunes),
	})
}

func contextStop(parent, runCtx context.Context) (StopReason, error) {
	if err := parent.Err(); err != nil {
		return classifyContextError(err)
	}
	if err := runCtx.Err(); err != nil {
		return classifyContextError(err)
	}
	return "", nil
}

func classifyContextError(err error) (StopReason, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return StopTimeout, fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return StopCanceled, fmt.Errorf("%w: %v", ErrCanceled, err)
}

func historyBytes(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}

func minStepsControl(minSteps, step int) string {
	return fmt.Sprintf(
		"<control reason=\"min_steps\">\n至少完成 %d 轮后才能结束（当前第 %d 轮）。请继续一步；如无需工具，请进一步核实后再次给出 final_answer。\n</control>",
		minSteps, step,
	)
}
