package cli

import (
	"fmt"
	"io"

	"simpleagent/internal/agent"
)

func newTraceFunc(w io.Writer) agent.TraceFunc {
	return func(event agent.TraceEvent) {
		writeTrace(w, event)
	}
}

func writeTrace(w io.Writer, event agent.TraceEvent) {
	switch event.Kind {
	case agent.TraceStep:
		fmt.Fprintf(w,
			"trace step=%d thought=%q action=%q input=%q final=%t\n",
			event.Step, event.Thought, event.Action, event.ActionInput, event.Final,
		)
	case agent.TraceTool:
		fmt.Fprintf(w,
			"trace tool step=%d action=%q status=%s observation=%q\n",
			event.Step, event.Action, event.Status, event.Observation,
		)
	case agent.TraceControl:
		fmt.Fprintf(w, "trace control step=%d text=%q\n", event.Step, event.Control)
	case agent.TraceStop:
		if event.Err != nil {
			fmt.Fprintf(w,
				"trace stop reason=%s steps=%d error=%q\n",
				event.StopReason, event.Step, sanitizeError(event.Err.Error()),
			)
			return
		}
		fmt.Fprintf(w, "trace stop reason=%s steps=%d\n", event.StopReason, event.Step)
	}
}
