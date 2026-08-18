package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tylergannon/tractor/engine"
	"github.com/tylergannon/tractor/graph"
)

type pluginConfig struct {
	Servers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		CWD     string   `json:"cwd"`
	} `json:"mcpServers"`
}

type schemaOutput struct {
	Schema string `json:"schema"`
}

type startOutput struct {
	RunID    string `json:"run_id"`
	PID      int    `json:"pid"`
	LogsRoot string `json:"logs_root"`
}

type statusOutput struct {
	Status      string `json:"status"`
	ExitCode    *int   `json:"exit_code"`
	CurrentNode string `json:"current_node"`
	NextNode    string `json:"next_node"`
}

type proof struct {
	PluginCommand     string   `json:"plugin_command"`
	ServerName        string   `json:"server_name"`
	ServerVersion     string   `json:"server_version"`
	Tools             []string `json:"tools"`
	AllDeferred       bool     `json:"all_deferred"`
	StartSchemaBytes  int      `json:"start_schema_bytes"`
	GraphSchemaSHA256 string   `json:"graph_schema_sha256"`
	SchemaMatches     bool     `json:"schema_matches_graph_type"`
	RunID             string   `json:"run_id"`
	RunPID            int      `json:"run_pid"`
	RunStatus         string   `json:"run_status"`
	ExitCode          int      `json:"exit_code"`
	CurrentNode       string   `json:"current_node"`
	NextNode          string   `json:"next_node"`
	CheckpointExists  bool     `json:"checkpoint_exists"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	repository, err := os.Getwd()
	if err != nil {
		return err
	}
	rawConfig, err := os.ReadFile(filepath.Join(repository, ".mcp.json"))
	if err != nil {
		return err
	}
	var config pluginConfig
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return err
	}
	serverConfig, ok := config.Servers["tractor"]
	if !ok {
		return errors.New(".mcp.json has no tractor server")
	}
	serverCWD := filepath.Join(repository, serverConfig.CWD)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	mcpClient, err := client.NewStdioMCPClientWithOptions(
		serverConfig.Command,
		nil,
		serverConfig.Args,
		transport.WithCommandFunc(func(ctx context.Context, command string, env, args []string) (*exec.Cmd, error) {
			process := exec.CommandContext(ctx, command, args...)
			process.Dir = serverCWD
			process.Env = append(os.Environ(), env...)
			return process, nil
		}),
	)
	if err != nil {
		return err
	}
	defer func() { _ = mcpClient.Close() }()

	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initialize.Params.ClientInfo = mcp.Implementation{Name: "tractor-live-proof", Version: "1.0.0"}
	initialized, err := mcpClient.Initialize(ctx, initialize)
	if err != nil {
		return err
	}
	listed, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return err
	}
	toolNames := make([]string, 0, len(listed.Tools))
	allDeferred := true
	startSchemaBytes := 0
	for _, tool := range listed.Tools {
		toolNames = append(toolNames, tool.Name)
		allDeferred = allDeferred && tool.DeferLoading
		if tool.Name == "start_run" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return err
			}
			startSchemaBytes = len(raw)
		}
	}
	slices.Sort(toolNames)
	if !allDeferred {
		return errors.New("one or more tools are not deferred")
	}

	schemaResult, err := call(ctx, mcpClient, "get_pipeline_schema", nil)
	if err != nil {
		return err
	}
	var schema schemaOutput
	if err := decode(schemaResult, &schema); err != nil {
		return err
	}
	wantSchema := graph.Graph{}.Schema()
	schemaHash := sha256.Sum256([]byte(schema.Schema))
	if schema.Schema != string(wantSchema) {
		return errors.New("MCP graph schema differs from graph.Graph{}.Schema()")
	}

	workdir := filepath.Join(repository, "ephemeral", "projects", "tractor", "mcp-plugin", "live-workspace")
	validation, err := call(ctx, mcpClient, "validate_pipeline", map[string]any{
		"pipeline_path": "pipeline.json",
		"workdir":       workdir,
	})
	if err != nil {
		return err
	}
	if validation.IsError {
		return fmt.Errorf("validation failed: %#v", validation.Content)
	}
	startedResult, err := call(ctx, mcpClient, "start_run", map[string]any{
		"pipeline_path": "pipeline.json",
		"workdir":       workdir,
	})
	if err != nil {
		return err
	}
	var started startOutput
	if err := decode(startedResult, &started); err != nil {
		return err
	}

	var status statusOutput
	for {
		statusResult, err := call(ctx, mcpClient, "get_run_status", map[string]any{"run_id": started.RunID})
		if err != nil {
			return err
		}
		if err := decode(statusResult, &status); err != nil {
			return err
		}
		if status.Status != "RUNNING" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if status.Status != "COMPLETED" || status.ExitCode == nil || *status.ExitCode != 0 {
		return fmt.Errorf("run did not complete successfully: %#v", status)
	}
	checkpoint, err := engine.LoadCheckpoint(started.LogsRoot)
	if err != nil {
		return err
	}

	result := proof{
		PluginCommand:     serverConfig.Command + " " + fmt.Sprint(serverConfig.Args),
		ServerName:        initialized.ServerInfo.Name,
		ServerVersion:     initialized.ServerInfo.Version,
		Tools:             toolNames,
		AllDeferred:       allDeferred,
		StartSchemaBytes:  startSchemaBytes,
		GraphSchemaSHA256: hex.EncodeToString(schemaHash[:]),
		SchemaMatches:     true,
		RunID:             started.RunID,
		RunPID:            started.PID,
		RunStatus:         status.Status,
		ExitCode:          *status.ExitCode,
		CurrentNode:       status.CurrentNode,
		NextNode:          status.NextNode,
		CheckpointExists:  checkpoint.CurrentNode == "done" && checkpoint.NextNode == "",
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func call(ctx context.Context, mcpClient *client.Client, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := mcpClient.CallTool(ctx, request)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, fmt.Errorf("%s returned a tool error: %#v", name, result.Content)
	}
	return result, nil
}

func decode(result *mcp.CallToolResult, output any) error {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}
