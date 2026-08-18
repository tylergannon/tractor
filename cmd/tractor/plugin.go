package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	tractorMarketplaceName   = "tractor"
	tractorMarketplaceSource = "tylergannon/tractor"
	tractorPluginSelector    = "tractor@tractor"
)

type pluginInstaller struct {
	run     func(string, ...string) ([]byte, error)
	homeDir func() (string, error)
	output  func(string, ...any)
}

type codexMarketplaceListing struct {
	Marketplaces []struct {
		Name string `json:"name"`
	} `json:"marketplaces"`
}

type codexPluginListing struct {
	Installed []struct {
		PluginID string `json:"pluginId"`
	} `json:"installed"`
}

func newPluginCommand() *cobra.Command {
	plugin := &cobra.Command{Use: "plugin", Short: "Manage the Tractor Codex plugin"}
	plugin.AddCommand(newPluginInstallCommand())
	return plugin
}

func newPluginInstallCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "install",
		Short: "Install or cleanly reinstall the Tractor Codex plugin",
		Args:  cobra.NoArgs,
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		installer := pluginInstaller{
			run:     runPluginCommand,
			homeDir: os.UserHomeDir,
			output: func(format string, arguments ...any) {
				_, _ = fmt.Fprintf(command.OutOrStdout(), format, arguments...)
			},
		}
		return installer.install()
	}
	return command
}

func runPluginCommand(name string, arguments ...string) ([]byte, error) {
	command := exec.Command(name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (i pluginInstaller) install() error {
	marketplacesRaw, err := i.run("codex", "plugin", "marketplace", "list", "--json")
	if err != nil {
		return err
	}
	var marketplaces codexMarketplaceListing
	if err := json.Unmarshal(marketplacesRaw, &marketplaces); err != nil {
		return fmt.Errorf("decode Codex marketplace list: %w", err)
	}
	marketplaceExists := false
	for _, marketplace := range marketplaces.Marketplaces {
		if marketplace.Name == tractorMarketplaceName {
			marketplaceExists = true
			break
		}
	}
	if marketplaceExists {
		if _, err := i.run("codex", "plugin", "marketplace", "upgrade", tractorMarketplaceName, "--json"); err != nil {
			return err
		}
	} else if _, err := i.run("codex", "plugin", "marketplace", "add", tractorMarketplaceSource, "--json"); err != nil {
		return err
	}

	pluginsRaw, err := i.run("codex", "plugin", "list", "--json")
	if err != nil {
		return err
	}
	var plugins codexPluginListing
	if err := json.Unmarshal(pluginsRaw, &plugins); err != nil {
		return fmt.Errorf("decode Codex plugin list: %w", err)
	}
	for _, plugin := range plugins.Installed {
		if plugin.PluginID == tractorPluginSelector {
			if _, err := i.run("codex", "plugin", "remove", tractorPluginSelector, "--json"); err != nil {
				return err
			}
			break
		}
	}
	if _, err := i.run("codex", "plugin", "add", tractorPluginSelector, "--json"); err != nil {
		return err
	}

	retired, err := retireDetachedMCPServers()
	if err != nil {
		return err
	}
	home, err := i.homeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory for legacy plugin cache cleanup: %w", err)
	}
	legacyCache := filepath.Join(home, ".cache", "tractor", "codex-plugin")
	if err := os.RemoveAll(legacyCache); err != nil {
		return fmt.Errorf("remove legacy Tractor plugin cache %q: %w", legacyCache, err)
	}
	i.output("installed %s; retired %d superseded MCP server(s); preserved detached runs\n", tractorPluginSelector, retired)
	i.output("start a new Codex task to use Tractor %s\n", tractorMCPVersion)
	return nil
}

func retireDetachedMCPServers() (int, error) {
	store, err := defaultMCPRunStore()
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(filepath.Dir(store.dir), "mcp-instances")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read MCP instance directory: %w", err)
	}
	retired := 0
	var cleanupErrors []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			cleanupErrors = append(cleanupErrors, readErr)
			continue
		}
		var instance mcpInstanceRecord
		if decodeErr := json.Unmarshal(raw, &instance); decodeErr != nil {
			_ = os.Remove(path)
			continue
		}
		if instance.PID <= 0 || instance.PID == os.Getpid() || !instance.DetachedRuns {
			continue
		}
		if !isProcessAlive(instance.PID) {
			_ = os.Remove(path)
			continue
		}
		if err := processOwnsMCPInstance(instance); err != nil {
			_ = os.Remove(path)
			continue
		}
		if err := syscall.Kill(instance.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stop superseded MCP server %d: %w", instance.PID, err))
			continue
		}
		deadline := time.Now().Add(time.Second)
		for isProcessAlive(instance.PID) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if isProcessAlive(instance.PID) {
			if err := syscall.Kill(instance.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("force-stop superseded MCP server %d: %w", instance.PID, err))
				continue
			}
		}
		_ = os.Remove(path)
		retired++
	}
	return retired, errors.Join(cleanupErrors...)
}

func processOwnsMCPInstance(instance mcpInstanceRecord) error {
	output, err := exec.Command("ps", "-p", fmt.Sprint(instance.PID), "-o", "command=").Output()
	if err != nil {
		return fmt.Errorf("inspect MCP server process %d: %w", instance.PID, err)
	}
	fields := strings.Fields(string(output))
	executableMatches := len(fields) >= 2 && (fields[0] == instance.ExecutablePath || filepath.Base(fields[0]) == filepath.Base(instance.ExecutablePath))
	if !executableMatches || fields[len(fields)-1] != "mcp" {
		return fmt.Errorf("process %d no longer matches its Tractor MCP instance record", instance.PID)
	}
	return nil
}
