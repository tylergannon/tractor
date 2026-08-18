package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/agy"
	"github.com/tylergannon/tractor/harness/claude"
	"github.com/tylergannon/tractor/harness/codex"
	"github.com/tylergannon/tractor/internal/runlog"
)

const (
	callerNone   = ""
	callerClaude = "claude"
	callerCodex  = "codex"
)

var outcomeSchema = json.RawMessage(`{
	"type":"object",
	"properties":{"next":{"type":"string"},"notes":{"type":"string"}},
	"required":["next","notes"],
	"additionalProperties":false
}`)

type options struct {
	provider        string
	model           string
	reasoningEffort string
	sessionID       string
	logsRoot        string
	timeout         time.Duration
}

type commandResult struct {
	Outcome   harness.Outcome `json:"outcome"`
	SessionID string          `json:"session_id"`
	LogsRoot  string          `json:"logs_root"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var values options
	flags.StringVar(&values.provider, "provider", "", "provider (standalone: openai, anthropic, or gemini)")
	flags.StringVar(&values.model, "model", "", "model (standalone required)")
	flags.StringVar(&values.reasoningEffort, "reasoning-effort", "", "reasoning effort (standalone required)")
	flags.StringVar(&values.sessionID, "session", "", "existing native session ID")
	flags.StringVar(&values.logsRoot, "logs", "", "run log directory (default: a new temporary directory)")
	flags.DurationVar(&values.timeout, "timeout", 20*time.Minute, "turn timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	positionals := flags.Args()
	if len(positionals) < 2 {
		return errors.New("usage: agent [flags] WORKDIR PROMPT")
	}
	selection, err := resolveSelection(detectCaller(os.Getenv), values)
	if err != nil {
		return err
	}
	workdir, err := filepath.Abs(positionals[0])
	if err != nil {
		return fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return fmt.Errorf("inspect workdir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workdir %q is not a directory", workdir)
	}
	if selection.logsRoot == "" {
		selection.logsRoot, err = os.MkdirTemp("", "tractor-agent-logs-")
		if err != nil {
			return fmt.Errorf("create logs directory: %w", err)
		}
	} else {
		selection.logsRoot, err = filepath.Abs(selection.logsRoot)
		if err != nil {
			return fmt.Errorf("resolve logs directory: %w", err)
		}
	}

	codexAdapter := codex.New()
	codexAdapter.SetStderr(os.Stderr)
	defer codexAdapter.Close()
	claudeAdapter := claude.New()
	claudeAdapter.SetStderr(os.Stderr)
	defer claudeAdapter.Close()
	agyAdapter := agy.New()
	agyAdapter.SetStderr(os.Stderr)
	defer agyAdapter.Close()
	harnessName, err := harnessForProvider(selection.provider)
	if err != nil {
		return err
	}
	bindings := map[string]harness.ThreadBinding(nil)
	if selection.sessionID != "" {
		bindings = map[string]harness.ThreadBinding{
			"agent": {Harness: harnessName, SessionID: selection.sessionID, Workdir: workdir},
		}
	}
	backend, backendErr := harness.NewHarnessBackend(
		selection.logsRoot,
		map[string]harness.HarnessAdapter{"agy": agyAdapter, "codex": codexAdapter, "claude": claudeAdapter},
		harness.DefaultProviderRoutes(),
		bindings,
	)
	if backendErr != nil {
		return backendErr
	}
	allocator, err := runlog.New(selection.logsRoot)
	if err != nil {
		return err
	}
	segment, err := allocator.Allocate("agent")
	if err != nil {
		return err
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-interrupts:
			backend.InterruptAll()
		case <-done:
		}
	}()

	outcome, runErr := backend.Run(harness.CodergenTurn{
		NodeID:          "agent",
		Parts:           []harness.ContentPart{{Type: harness.ContentPartText, Text: strings.Join(positionals[1:], " ")}},
		OutputSchema:    outcomeSchema,
		Model:           selection.model,
		Provider:        selection.provider,
		ReasoningEffort: selection.reasoningEffort,
		Fidelity:        harness.FidelityFull,
		ThreadKey:       "agent",
		Workdir:         workdir,
		RunLog:          segment.Path,
		Timeout:         selection.timeout,
	})
	if runErr != nil {
		return runErr
	}
	binding := backend.Bindings()["agent"]
	return json.NewEncoder(os.Stdout).Encode(commandResult{
		Outcome:   outcome,
		SessionID: binding.SessionID,
		LogsRoot:  selection.logsRoot,
	})
}

func detectCaller(getenv func(string) string) string {
	if strings.TrimSpace(getenv("CLAUDE_CODE_SESSION_ID")) != "" {
		return callerClaude
	}
	if strings.TrimSpace(getenv("CODEX_THREAD_ID")) != "" {
		return callerCodex
	}
	return callerNone
}

func resolveSelection(caller string, values options) (options, error) {
	switch caller {
	case callerClaude:
		values.provider = "openai"
		values.model = "gpt-5.6-sol"
		values.reasoningEffort = "medium"
	case callerCodex:
		values.provider = "anthropic"
		values.model = "fable"
		values.reasoningEffort = "medium"
	case callerNone:
		if strings.TrimSpace(values.provider) == "" || strings.TrimSpace(values.model) == "" || strings.TrimSpace(values.reasoningEffort) == "" {
			return options{}, errors.New("standalone use requires --provider, --model, and --reasoning-effort")
		}
	default:
		return options{}, fmt.Errorf("unknown caller %q", caller)
	}
	if _, err := harnessForProvider(values.provider); err != nil {
		return options{}, err
	}
	if values.timeout < 0 {
		return options{}, errors.New("timeout must not be negative")
	}
	return values, nil
}

func harnessForProvider(provider string) (string, error) {
	switch provider {
	case "openai":
		return "codex", nil
	case "anthropic":
		return "claude", nil
	case "gemini":
		return "agy", nil
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
}
