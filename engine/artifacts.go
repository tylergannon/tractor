package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tylergannon/tractor/graph"
)

// BranchArtifact is one declared branch output exposed to fan-in and later
// stages. Path is stable run evidence in isolated mode and the live workspace
// path in shared mode.
type BranchArtifact struct {
	Declared string `json:"declared"`
	Path     string `json:"path"`
	Source   string `json:"source,omitempty"`
}

func (h *parallelHandler) collectArtifacts(stageDir string, parallel *graph.ParallelNode, results []BranchResult) {
	for index := range results {
		result := &results[index]
		if result.Error != nil {
			continue
		}
		branch, ok := parallel.Branch(result.BranchID)
		if !ok || branch.IsLegacy() {
			continue
		}
		result.Workspace = string(parallel.WorkspacePolicyValue())
		for _, declared := range branch.Artifacts {
			artifact, err := collectBranchArtifact(stageDir, parallel.WorkspacePolicyValue(), *result, declared)
			if err != nil {
				result.Outcome = nil
				result.Error = terminalError(fmt.Sprintf("collect artifact %q for branch %q: %v", declared, result.BranchID, err))
				break
			}
			result.Artifacts = append(result.Artifacts, artifact)
		}
	}
}

func collectBranchArtifact(stageDir string, policy graph.WorkspacePolicy, result BranchResult, declared string) (BranchArtifact, error) {
	source := filepath.Join(result.Workdir, filepath.Clean(declared))
	info, err := os.Lstat(source)
	if err != nil {
		return BranchArtifact{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return BranchArtifact{}, fmt.Errorf("symbolic links are not supported")
	}
	if policy == graph.WorkspaceShared {
		return BranchArtifact{Declared: declared, Path: source}, nil
	}
	target := filepath.Join(stageDir, "artifacts", result.BranchID, filepath.Clean(declared))
	if err := copyArtifact(source, target, info); err != nil {
		return BranchArtifact{}, err
	}
	return BranchArtifact{Declared: declared, Path: target, Source: source}, nil
}

func copyArtifact(source, target string, info os.FileInfo) error {
	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			entrySource := filepath.Join(source, entry.Name())
			entryInfo, err := os.Lstat(entrySource)
			if err != nil {
				return err
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic links are not supported: %s", entrySource)
			}
			if err := copyArtifact(entrySource, filepath.Join(target, entry.Name()), entryInfo); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported artifact file mode %s", info.Mode())
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
