package sprint

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergannon/gimble"
)

type taskAdapter struct {
	prompts []string
}

func (a *taskAdapter) CreateSession(context.Context, string, string) (string, error) {
	return "session", nil
}

func (a *taskAdapter) RunTurn(_ context.Context, _ string, prompt string, schema json.RawMessage, _ func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	a.prompts = append(a.prompts, prompt)
	if len(schema) != 0 {
		return gimble.TurnResult{Output: json.RawMessage(`{"objections":["The task has no evidence for its definition of done."]}`)}, nil
	}
	out, err := json.Marshal("worker finished")
	return gimble.TurnResult{Output: out}, err
}

func (*taskAdapter) Steer(context.Context, string, string) error  { return nil }
func (*taskAdapter) Fork(context.Context, string) (string, error) { return "fork", nil }
func (*taskAdapter) Close(context.Context, string) error          { return nil }

func TestRunTaskAssessesDefinitionOfDoneWithoutValidationRecipe(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "before"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "before")
	runGit(t, repo, "commit", "-qm", "before")

	oldChecks := checks
	checks = nil
	t.Cleanup(func() { checks = oldChecks })

	adapter := &taskAdapter{}
	err := gimble.Run(gimble.Project(t.Context(), t.TempDir()), "test", func(ctx context.Context) error {
		researcher := gimble.NewSession(ctx, "researcher", adapter, "test", repo)
		validator := gimble.NewSession(ctx, "validator", adapter, "test", repo)
		return runTask(ctx, Input{Sprint: 1, Repo: repo, ReviewModel: "test"}, researcher, validator, adapter, gimble.Task{
			Name:             "prove-result",
			Description:      "Produce the expected result.",
			DefinitionOfDone: "The expected result is demonstrated.",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(adapter.prompts) != 2 {
		t.Fatalf("turns = %d, want worker plus task assessment", len(adapter.prompts))
	}
	if !strings.Contains(adapter.prompts[1], "Definition of done: The expected result is demonstrated.") {
		t.Fatalf("assessment prompt omits task definition of done:\n%s", adapter.prompts[1])
	}
	if got := runGit(t, repo, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count = %s, want 1: an objection must block the task commit", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
