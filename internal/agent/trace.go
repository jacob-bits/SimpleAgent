package agent

// TraceKind identifies one structured Agent trace event.
type TraceKind string

const (
	TraceStep    TraceKind = "step"
	TraceTool    TraceKind = "tool"
	TraceControl TraceKind = "control"
	TraceStop    TraceKind = "stop"
)

const (
	traceThoughtRunes     = 200
	traceInputRunes       = 256
	traceObservationRunes = 256
	traceControlRunes     = 256
)

// TraceEvent is a structured, already-truncated trace record.
//
// Err is non-nil only for TraceStop events that end the run with an error.
type TraceEvent struct {
	Kind        TraceKind
	Step        int
	Thought     string
	Action      string
	ActionInput string
	Final       bool
	Status      string
	Observation string
	Control     string
	StopReason  StopReason
	Err         error
}

// TraceFunc consumes one trace event. The CLI may format it to stderr when
// agent.verbose is enabled. Implementations must be concurrency-safe if the
// same Agent is used from multiple goroutines.
type TraceFunc func(TraceEvent)

// WithTrace installs a trace sink. A nil function disables tracing.
func WithTrace(fn TraceFunc) Option {
	return func(a *Agent) {
		a.trace = fn
	}
}
