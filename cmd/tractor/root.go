package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tylergannon/tractor/engine"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/claude"
	"github.com/tylergannon/tractor/harness/codex"
	"github.com/tylergannon/tractor/lint"
)

const (
	defaultModel           = "gpt-5.6-sol"
	defaultReasoningEffort = "high"
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:               "tractor",
		Short:             "Run Tractor pipelines",
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.AddCommand(newValidateCommand(), newRunCommand(), newPrintSchemaCommand(), newMCPCommand())
	return root
}

func newValidateCommand() *cobra.Command {
	var inlineJSON string
	var inlineYAML string
	command := &cobra.Command{
		Use:   "validate [pipeline]",
		Short: "Parse and validate a pipeline",
		RunE: func(command *cobra.Command, args []string) error {
			pipeline, source, err := loadPipeline(
				args,
				inlineJSON, command.Flags().Changed("json"),
				inlineYAML, command.Flags().Changed("yaml"),
			)
			if err != nil {
				return err
			}
			if err := validateAndReport(command, cliValidator(), *pipeline); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "valid %s\n", source)
			return err
		},
	}
	command.Flags().StringVar(&inlineJSON, "json", "", "pipeline JSON")
	command.Flags().StringVar(&inlineYAML, "yaml", "", "pipeline YAML")
	return command
}

func newRunCommand() *cobra.Command {
	var inlineJSON string
	var inlineYAML string
	var workdir string
	var logsRoot string
	var resume bool
	command := &cobra.Command{
		Use:   "run [pipeline]",
		Short: "Run a pipeline",
		RunE: func(command *cobra.Command, args []string) error {
			pipeline, _, err := loadPipeline(
				args,
				inlineJSON, command.Flags().Changed("json"),
				inlineYAML, command.Flags().Changed("yaml"),
			)
			if err != nil {
				return err
			}
			return runPipeline(command, *pipeline, workdir, logsRoot, resume)
		},
	}
	command.Flags().StringVar(&inlineJSON, "json", "", "pipeline JSON")
	command.Flags().StringVar(&inlineYAML, "yaml", "", "pipeline YAML")
	command.Flags().StringVar(&workdir, "workdir", ".", "pipeline workspace")
	command.Flags().StringVar(&logsRoot, "logs", "", "run log directory")
	command.Flags().BoolVar(&resume, "resume", false, "resume from the logs checkpoint")
	return command
}

func newPrintSchemaCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "print-schema",
		Short: "Print the pipeline JSON Schema",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := command.OutOrStdout().Write(graph.Graph{}.Schema())
			return err
		},
	}
}

func loadPipeline(args []string, inlineJSON string, jsonSet bool, inlineYAML string, yamlSet bool) (*graph.Graph, string, error) {
	if len(args) > 1 {
		return nil, "", fmt.Errorf("accepts exactly one pipeline source")
	}
	if (jsonSet && yamlSet) || (len(args) == 1 && (jsonSet || yamlSet)) {
		return nil, "", fmt.Errorf("pipeline file, --json, and --yaml are mutually exclusive")
	}
	if !jsonSet && !yamlSet && len(args) == 0 {
		return nil, "", fmt.Errorf("pipeline source is required: provide a file, --json, or --yaml")
	}
	if jsonSet {
		pipeline, err := graph.Parse([]byte(inlineJSON))
		return pipeline, "--json", err
	}
	if yamlSet {
		pipeline, err := graph.ParseYAML([]byte(inlineYAML))
		return pipeline, "--yaml", err
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return nil, "", fmt.Errorf("read pipeline %q: %w", args[0], err)
	}
	var pipeline *graph.Graph
	if extension := strings.ToLower(filepath.Ext(args[0])); extension == ".yaml" || extension == ".yml" {
		pipeline, err = graph.ParseYAML(raw)
	} else {
		pipeline, err = graph.Parse(raw)
	}
	return pipeline, args[0], err
}

func runPipeline(command *cobra.Command, pipeline graph.Graph, workdir, logsRoot string, resume bool) error {
	if strings.TrimSpace(logsRoot) == "" {
		return fmt.Errorf("--logs is required")
	}
	workdir, err := absoluteDirectory(workdir)
	if err != nil {
		return err
	}
	logsRoot, err = filepath.Abs(logsRoot)
	if err != nil {
		return fmt.Errorf("resolve logs directory: %w", err)
	}
	validator := cliValidator()
	if err := validateAndReport(command, validator, pipeline); err != nil {
		return err
	}

	var bindings map[string]harness.ThreadBinding
	if resume {
		checkpoint, err := engine.LoadCheckpoint(logsRoot)
		if err != nil {
			return err
		}
		bindings = checkpoint.Sessions
	}
	codexAdapter := codex.New()
	codexAdapter.SetStderr(command.ErrOrStderr())
	defer codexAdapter.Close()
	claudeAdapter := claude.New()
	claudeAdapter.SetStderr(command.ErrOrStderr())
	defer claudeAdapter.Close()
	backend, backendErr := harness.NewHarnessBackend(
		logsRoot,
		map[string]harness.HarnessAdapter{"codex": codexAdapter, "claude": claudeAdapter},
		harness.DefaultProviderRoutes(),
		bindings,
	)
	if backendErr != nil {
		return backendErr
	}

	registry := engine.NewRegistry()
	codergenConfig := engine.CodergenConfig{
		Backend:                backend,
		DefaultModel:           defaultModel,
		DefaultReasoningEffort: defaultReasoningEffort,
	}
	registry.Register("codergen", engine.NewCodergenHandler(codergenConfig))
	registry.Register("parallel.fan_in", engine.NewFanInHandler(codergenConfig))
	runnerConfig := engine.RunnerConfig{
		LogsRoot:               logsRoot,
		Workdir:                workdir,
		DefaultModel:           defaultModel,
		DefaultReasoningEffort: defaultReasoningEffort,
		Validate: func(candidate graph.Graph) error {
			_, err := validator.ValidateOrError(candidate)
			return err
		},
		Backend: backend,
	}
	var runner *engine.Runner
	if resume {
		runner, err = engine.ResumeRunner(pipeline, registry, runnerConfig)
	} else {
		runner, err = engine.NewRunner(pipeline, registry, runnerConfig)
	}
	if err != nil {
		return err
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			runner.Stop()
		case <-done:
		}
	}()
	result, err := runner.Run()
	if err != nil {
		return err
	}
	if result.Status != engine.RunCompleted {
		return fmt.Errorf("pipeline failed: %s", result.FailureReason)
	}
	_, err = fmt.Fprintln(command.OutOrStdout(), result.Status)
	return err
}

func absoluteDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect workdir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %q is not a directory", absolute)
	}
	return absolute, nil
}

func cliValidator() *lint.Validator {
	return lint.New(lint.Options{ResolveHarness: resolveHarness})
}

func resolveHarness(provider, model string) (string, error) {
	if provider == "" {
		if model == "" {
			model = defaultModel
		}
		provider = engine.DetectProvider(model)
	}
	harnessName := harness.DefaultProviderRoutes()[provider]
	if harnessName == "" {
		return "", fmt.Errorf("no harness route for provider %q", provider)
	}
	return harnessName, nil
}

func validateAndReport(command *cobra.Command, validator *lint.Validator, pipeline graph.Graph) error {
	diagnostics := validator.Validate(pipeline)
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == lint.SeverityError {
			continue
		}
		if _, err := fmt.Fprintf(command.ErrOrStderr(), "%s %s: %s\n", diagnostic.Severity, diagnostic.Rule, diagnostic.Message); err != nil {
			return err
		}
	}
	if lint.HasErrors(diagnostics) {
		return &lint.ValidationError{Diagnostics: diagnostics}
	}
	return nil
}
