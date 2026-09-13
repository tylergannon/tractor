package gimble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tylergannon/polytype"
)

// Task is one assignment selected by a Loop planner.
type Task struct {
	// Name is a short label for recognizing the work.
	Name string `json:"name"`
	// Description states the desired result and any necessary, non-obvious
	// information. It leaves the approach to the worker.
	Description string `json:"description"`
	// DefinitionOfDone says how to recognize successful completion of this
	// assignment. It does not declare that the enclosing goal is complete.
	DefinitionOfDone string `json:"definition_of_done"`
	// Validation describes evidence the workflow can gather. Either field may
	// be empty; the workflow still assesses the task against DefinitionOfDone.
	Validation struct {
		// Command is a known executable check.
		Command string `json:"command"`
		// Query is a question for a validator agent.
		Query string `json:"query"`
	} `json:"validation"`
}

// plan is the planner's whole answer for one dispatch: the revised backlog
// and the index of the next task, or null to end dispatch.
type plan struct {
	Tasks []Task                 `json:"tasks"`
	Next  polytype.Nullable[int] `json:"next"`
}

type loop struct {
	ctx     context.Context
	name    string
	goal    string
	planner *Session
	err     error
}

// Loop opens planner-directed dispatch for goal. The planner is the Session
// chosen and prepared by the workflow. Loop keeps a revisable backlog in its
// run scope and uses values recorded by each task as feedback for the next
// decision. Range over its Tasks method and check Err afterward.
func Loop(ctx context.Context, name, goal string, planner *Session) *loop {
	return &loop{ctx: ctx, name: name, goal: goal, planner: planner}
}

// Tasks yields planner-selected assignments. Each ctx is a child scope that
// contains the structured task and ends when the loop body returns. Values the
// body records in that scope are shown to the planner before its next decision,
// alongside the values currently visible from the loop's parent scopes.
//
// The planner may revise, reorder, and extend the backlog as work reveals what
// matters. It ends dispatch by returning no task. That decision is distinct
// from validation and from fulfillment of the enclosing goal.
func (l *loop) Tasks(yield func(context.Context, Task) bool) {
	parent, err := current(l.ctx)
	if err != nil {
		l.err = err
		return
	}
	l.err = parent.child(l.name).do(l.ctx, func(ctx context.Context) error {
		loopScope, _ := current(ctx)
		dir := filepath.Join(loopScope.run.dir, "scopes", filepath.FromSlash(loopScope.key))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("gimble: %w", err)
		}
		file := filepath.Join(dir, "backlog.md")

		tasks := []Task{}
		var previous string
		for {
			backlogText, err := backlogJSON(l.goal, tasks)
			if err != nil {
				return fmt.Errorf("gimble: %w", err)
			}
			p, err := l.planner.Generate[plan](ctx, planPrompt(l.name, l.planner.workdir, string(backlogText), ScopeText(ctx), previous))
			if err != nil {
				return err
			}
			if err := validatePlan(p); err != nil {
				return fmt.Errorf("gimble: loop %q: %w", l.name, err)
			}
			tasks = p.Tasks
			if tasks == nil {
				tasks = []Task{}
			}

			revisedText, err := backlogJSON(l.goal, tasks)
			if err != nil {
				return fmt.Errorf("gimble: %w", err)
			}
			if err := os.WriteFile(file, []byte("---\n"+string(revisedText)+"\n---\n"), 0o644); err != nil {
				return fmt.Errorf("gimble: %w", err)
			}

			if !p.Next.Present {
				loopScope.run.event(loopScope.key, "", "", PlannerDecision{})
				logf("%s: the planner ended dispatch", loopScope.key)
				return nil
			}

			task := tasks[p.Next.Value]
			logf("%s: task: %s", loopScope.key, oneLine(task.Name))
			loopScope.run.event(loopScope.key, "", "", PlannerDecision{Task: optionalTask(task)})
			more := true
			taskScope := loopScope.child("task")
			taskCtx := context.WithValue(ctx, taskKey{}, task)
			if err := taskScope.do(taskCtx, func(ctx context.Context) error {
				raw, err := json.Marshal(task)
				if err != nil {
					return fmt.Errorf("gimble: encode task: %w", err)
				}
				if err := store(ctx, "task", raw); err != nil {
					return err
				}
				more = yield(ctx, task)
				previous = taskScope.localText()
				return nil
			}); err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	})
}

// Err returns the error that ended dispatch, if any: persistence, malformed
// planner data, cancellation, or the planner's harness. A failed task
// validation recorded by the workflow is feedback, not a Loop error.
func (l *loop) Err() error {
	return l.err
}

// backlogJSON renders goal and tasks as the JSON object written to
// backlog.md (inside its frontmatter) and shown to the planner in its
// prompt, so both channels always agree.
func backlogJSON(goal string, tasks []Task) ([]byte, error) {
	return json.MarshalIndent(struct {
		Goal  string `json:"goal"`
		Tasks []Task `json:"tasks"`
	}{Goal: goal, Tasks: tasks}, "", "  ")
}

// validatePlan checks a planner's structured answer: every task must be
// well-formed, no two tasks may share a name, and a present Next must index
// into Tasks.
func validatePlan(p plan) error {
	seen := make(map[string]bool, len(p.Tasks))
	for i, task := range p.Tasks {
		if err := validateTask(task); err != nil {
			return fmt.Errorf("task %d: %w", i+1, err)
		}
		name := strings.TrimSpace(task.Name)
		if seen[name] {
			return fmt.Errorf("task %d: duplicate task name %q", i+1, task.Name)
		}
		seen[name] = true
	}
	if p.Next.Present && (p.Next.Value < 0 || p.Next.Value >= len(p.Tasks)) {
		return fmt.Errorf("next %d is out of range for %d tasks", p.Next.Value, len(p.Tasks))
	}
	return nil
}

func validateTask(task Task) error {
	switch {
	case strings.TrimSpace(task.Name) == "":
		return errors.New("name is blank")
	case strings.TrimSpace(task.Description) == "":
		return errors.New("description is blank")
	case strings.TrimSpace(task.DefinitionOfDone) == "":
		return errors.New("definition of done is blank")
	default:
		return nil
	}
}

func planPrompt(name, workdir, backlogText, scoped, previous string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You plan the loop %q in %s. Its backlog is shown below.\n\n", name, workdir)
	b.WriteString("Choose the next assignment that offers the greatest concrete gain toward the goal, based on current evidence, priorities, and real dependencies. Size it for one worker to understand, complete, and demonstrate in one working session. A later task may offer more gain than repairing a nonblocking earlier defect; keep deferred defects visible.\n\n")
	b.WriteString("Treat recorded deterministic results as authoritative: a prose claim or agent judgment cannot override a nonzero command exit. If a check relevant to the goal or an assignment's Definition of Done failed and no later recorded run passed, work remains.\n\n")
	b.WriteString("Inspect the workspace only to plan; do not perform or validate an assignment yourself.\n\n")
	b.WriteString("Describe the desired result and necessary non-obvious facts. Trust the worker to choose the approach. Do not supply procedural checklists, obvious advice, speculative code, or a numerical progress score.\n\n")
	if strings.TrimSpace(scoped) != "" {
		b.WriteString("Scoped context:\n\n" + scoped + "\n\n")
	}
	if strings.TrimSpace(previous) != "" {
		b.WriteString("Previous task record:\n\n" + previous + "\n\n")
	}
	b.WriteString("Backlog now:\n\n" + backlogText + "\n\n")
	b.WriteString("Return the full revised task list in `tasks` and the index of the chosen task in `next`, or `next: null` to end dispatch; that does not certify that the goal is fulfilled.")
	return b.String()
}
