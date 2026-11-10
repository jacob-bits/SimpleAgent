package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"simpleagent/internal/agent"
	"simpleagent/internal/config"
	"simpleagent/internal/llm"
	"simpleagent/internal/tool"
	"simpleagent/internal/tool/readfile"
)

const (
	exitOK   = 0
	exitRun  = 1
	exitUse  = 2
	flagDone = -1

	maxQuestionBytes = 64 << 10
	maxErrorRunes    = 1024
)

var errQuestionRequired = errors.New("question is required (provide an argument or pipe it via stdin)")

type flagOptions struct {
	config string

	baseURL         string
	model           string
	temperature     float64
	maxTokens       int
	llmTimeout      int
	reasoningEffort string

	minSteps            int
	maxSteps            int
	maxRepeatedActions  int
	noProgressThreshold float64
	noProgressMinChars  int
	maxHistoryChars     int
	timeoutSeconds      int
	verbose             bool

	allowedRoot  string
	maxFileBytes int64
}

type parsedFlags struct {
	options  flagOptions
	visited  map[string]bool
	question []string
	fs       *flag.FlagSet
}

// Run executes the CLI and returns a process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	parsed, code := parseFlags(args, stdout, stderr)
	if code != flagDone {
		return code
	}

	cfg, err := config.Load(parsed.options.config)
	if err != nil {
		printError(stderr, err)
		return exitUse
	}
	applyOverrides(cfg, parsed.options, parsed.visited)
	if err := cfg.Validate(); err != nil {
		printError(stderr, err)
		return exitUse
	}

	question, err := resolveQuestion(stdin, parsed.question)
	if err != nil {
		printError(stderr, err)
		printUsage(stderr, parsed.fs)
		return exitUse
	}

	reader, err := readfile.New(cfg.Tools.AllowedRoot, cfg.Tools.MaxFileBytes)
	if err != nil {
		printError(stderr, err)
		return exitUse
	}
	client, err := llm.New(cfg.LLM)
	if err != nil {
		printError(stderr, err)
		return exitUse
	}
	registry, err := tool.NewRegistry(reader)
	if err != nil {
		printError(stderr, err)
		return exitUse
	}

	agentOptions := make([]agent.Option, 0, 1)
	if cfg.Agent.Verbose {
		agentOptions = append(agentOptions, agent.WithTrace(newTraceFunc(stderr)))
	}
	runner, err := agent.New(cfg.Agent, client, registry, agentOptions...)
	if err != nil {
		printError(stderr, err)
		return exitUse
	}

	warnMissingAPIKey(stderr, cfg)

	result, err := runner.Run(ctx, question)
	if err != nil {
		printError(stderr, err)
		return exitRun
	}
	if result.Reason != agent.StopFinalAnswer {
		printError(stderr, fmt.Errorf("unexpected stop reason %q", result.Reason))
		return exitRun
	}
	if err := writeAnswer(stdout, result.Answer); err != nil {
		printError(stderr, fmt.Errorf("write answer: %w", err))
		return exitRun
	}
	return exitOK
}

func parseFlags(args []string, stdout, stderr io.Writer) (*parsedFlags, int) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var opts flagOptions
	registerFlags(fs, &opts)

	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		printUsage(stdout, fs)
		return nil, exitOK
	case err != nil:
		printError(stderr, err)
		printUsage(stderr, fs)
		return nil, exitUse
	}

	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return &parsedFlags{
		options:  opts,
		visited:  visited,
		question: fs.Args(),
		fs:       fs,
	}, flagDone
}

func registerFlags(fs *flag.FlagSet, opts *flagOptions) {
	fs.StringVar(&opts.config, "config", "", "JSON configuration file; empty uses built-in defaults")
	fs.StringVar(&opts.baseURL, "base-url", "", "OpenAI-compatible API base URL")
	fs.StringVar(&opts.model, "model", "", "model name")
	fs.Float64Var(&opts.temperature, "temperature", 0, "sampling temperature")
	fs.IntVar(&opts.maxTokens, "max-tokens", 0, "completion token limit; 0 means provider default")
	fs.IntVar(&opts.llmTimeout, "llm-timeout", 0, "single LLM request timeout in seconds")
	fs.StringVar(&opts.reasoningEffort, "reasoning-effort", "", "reasoning effort; empty omits the field")

	fs.IntVar(&opts.minSteps, "min-steps", 0, "minimum steps before final_answer is accepted")
	fs.IntVar(&opts.maxSteps, "max-steps", 0, "maximum ReAct decision steps")
	fs.IntVar(&opts.maxRepeatedActions, "max-repeated-actions", 0, "consecutive identical action limit")
	fs.Float64Var(&opts.noProgressThreshold, "no-progress-threshold", 0, "observation similarity threshold; 0 disables")
	fs.IntVar(&opts.noProgressMinChars, "no-progress-min-chars", 0, "short-observation exact-equality threshold")
	fs.IntVar(&opts.maxHistoryChars, "max-history-chars", 0, "history byte budget")
	fs.IntVar(&opts.timeoutSeconds, "timeout", 0, "total Agent timeout in seconds")
	fs.BoolVar(&opts.verbose, "verbose", false, "write structured trace to stderr")

	fs.StringVar(&opts.allowedRoot, "allowed-root", "", "root directory readable by the file tool")
	fs.Int64Var(&opts.maxFileBytes, "max-file-bytes", 0, "maximum file size in bytes")
}

func applyOverrides(cfg *config.Config, opts flagOptions, visited map[string]bool) {
	if visited["base-url"] {
		cfg.LLM.BaseURL = opts.baseURL
	}
	if visited["model"] {
		cfg.LLM.Model = opts.model
	}
	if visited["temperature"] {
		cfg.LLM.Temperature = opts.temperature
	}
	if visited["max-tokens"] {
		cfg.LLM.MaxTokens = opts.maxTokens
	}
	if visited["llm-timeout"] {
		cfg.LLM.TimeoutSeconds = opts.llmTimeout
	}
	if visited["reasoning-effort"] {
		cfg.LLM.ReasoningEffort = opts.reasoningEffort
	}

	if visited["min-steps"] {
		cfg.Agent.MinSteps = opts.minSteps
	}
	if visited["max-steps"] {
		cfg.Agent.MaxSteps = opts.maxSteps
	}
	if visited["max-repeated-actions"] {
		cfg.Agent.MaxRepeatedActions = opts.maxRepeatedActions
	}
	if visited["no-progress-threshold"] {
		cfg.Agent.NoProgressThreshold = opts.noProgressThreshold
	}
	if visited["no-progress-min-chars"] {
		cfg.Agent.NoProgressMinChars = opts.noProgressMinChars
	}
	if visited["max-history-chars"] {
		cfg.Agent.MaxHistoryChars = opts.maxHistoryChars
	}
	if visited["timeout"] {
		cfg.Agent.TimeoutSeconds = opts.timeoutSeconds
	}
	if visited["verbose"] {
		cfg.Agent.Verbose = opts.verbose
	}

	if visited["allowed-root"] {
		cfg.Tools.AllowedRoot = opts.allowedRoot
	}
	if visited["max-file-bytes"] {
		cfg.Tools.MaxFileBytes = opts.maxFileBytes
	}
}

func resolveQuestion(stdin io.Reader, positional []string) (string, error) {
	if len(positional) > 0 {
		question := strings.TrimSpace(strings.Join(positional, " "))
		if question == "" {
			return "", errQuestionRequired
		}
		return question, nil
	}
	if stdin == nil || isTerminal(stdin) {
		return "", errQuestionRequired
	}

	data, err := io.ReadAll(io.LimitReader(stdin, maxQuestionBytes+1))
	if err != nil {
		return "", fmt.Errorf("read question from stdin: %w", err)
	}
	if len(data) > maxQuestionBytes {
		return "", fmt.Errorf("question from stdin exceeds %d bytes", maxQuestionBytes)
	}
	question := strings.TrimSpace(string(data))
	if question == "" {
		return "", errQuestionRequired
	}
	return question, nil
}

func isTerminal(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage: agent [flags] [question]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run one ReAct question through the configured LLM and tools.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "If no question argument is provided and stdin is not a terminal, stdin is read as the question (maximum %d bytes).\n", maxQuestionBytes)
	fmt.Fprintln(w, "Positional arguments take precedence over stdin.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  agent -config config.example.json \"根据 examples/notes.txt，项目代号是什么？\"")
	fmt.Fprintln(w, "  agent -config config.example.json -verbose \"主要风险是什么？\"")
	fmt.Fprintln(w, "  cat question.txt | agent -config config.example.json")
	fmt.Fprintln(w, "  agent -- \"-question text\"")
}

func printError(w io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(w, "error: %s\n", sanitizeError(err.Error()))
}

func sanitizeError(message string) string {
	var b strings.Builder
	count := 0
	for _, r := range message {
		if count >= maxErrorRunes {
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
		count++
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized == "" {
		return "unknown error"
	}
	return sanitized
}

func writeAnswer(w io.Writer, answer string) error {
	if !strings.HasSuffix(answer, "\n") {
		answer += "\n"
	}
	n, err := io.WriteString(w, answer)
	if err != nil {
		return err
	}
	if n != len(answer) {
		return io.ErrShortWrite
	}
	return nil
}

func warnMissingAPIKey(w io.Writer, cfg *config.Config) {
	if cfg.LLM.ResolveAPIKey() != "" {
		return
	}
	if cfg.LLM.APIKeyEnv == "" {
		fmt.Fprintln(w, "warning: no LLM API key configured; requests may fail with 401")
		return
	}
	fmt.Fprintf(w,
		"warning: no LLM API key configured (set llm.api_key or %s); requests may fail with 401\n",
		sanitizeError(cfg.LLM.APIKeyEnv),
	)
}
