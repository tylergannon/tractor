package gimble_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tylergannon/gimble"
)

type exampleAdapter struct{}

func (*exampleAdapter) CreateSession(context.Context, string, string) (string, error) {
	return "example-session", nil
}

func (a *exampleAdapter) RunTurn(_ context.Context, _ string, prompt string, schema json.RawMessage, emit func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	out, err := json.Marshal("done")
	return gimble.TurnResult{Output: out}, err
}

func (*exampleAdapter) Steer(context.Context, string, string) error { return nil }
func (*exampleAdapter) Fork(context.Context, string) (string, error) {
	return "example-fork", nil
}
func (*exampleAdapter) Close(context.Context, string) error { return nil }

func exampleContext() (context.Context, func()) {
	dir, err := os.MkdirTemp("", "gimble-example-")
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return gimble.Project(ctx, dir), func() {
		cancel()
		_ = os.RemoveAll(dir)
	}
}

func Example() {
	ctx, closeProject := exampleContext()
	defer closeProject()

	err := gimble.Run(ctx, "example", func(ctx context.Context) error {
		if err := gimble.Set(ctx, "goal", "demonstrate the public API"); err != nil {
			return err
		}
		worker := gimble.NewSession(ctx, "worker", &exampleAdapter{}, "example", ".")
		answer, err := worker.Generate[gimble.Text](ctx,
			"Complete the goal.\n\n"+gimble.ScopeText(ctx))
		if err != nil {
			return err
		}
		fmt.Println(answer)
		return nil
	})
	fmt.Println(err)

	// Output:
	// done
	// <nil>
}

func ExampleGroup() {
	ctx, closeProject := exampleContext()
	defer closeProject()

	err := gimble.Run(ctx, "parallel", func(ctx context.Context) error {
		group := gimble.Group(ctx, "drafts")
		group.Go("draft", func(ctx context.Context) error {
			return gimble.Set(ctx, "approach", "first")
		})
		group.Go("draft", func(ctx context.Context) error {
			return gimble.Set(ctx, "approach", "second")
		})
		return group.Wait()
	})
	fmt.Println(err)

	// Output: <nil>
}

type exampleLoopAdapter struct{ turns int }

func (*exampleLoopAdapter) CreateSession(context.Context, string, string) (string, error) {
	return "example-planner", nil
}

func (a *exampleLoopAdapter) RunTurn(_ context.Context, _ string, _ string, _ json.RawMessage, _ func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	a.turns++
	if a.turns > 1 {
		return gimble.TurnResult{Output: json.RawMessage(`{"tasks":[],"next":null}`)}, nil
	}
	return gimble.TurnResult{Output: json.RawMessage(`{"tasks":[{"name":"Show the task","description":"Make the structured assignment visible to the workflow.","definition_of_done":"The workflow receives and records the assignment.","validation":{"command":"","query":""}}],"next":0}`)}, nil
}

func (*exampleLoopAdapter) Steer(context.Context, string, string) error { return nil }
func (*exampleLoopAdapter) Fork(context.Context, string) (string, error) {
	return "example-planner-fork", nil
}
func (*exampleLoopAdapter) Close(context.Context, string) error { return nil }

func ExampleLoop() {
	ctx, closeProject := exampleContext()
	defer closeProject()

	err := gimble.Run(ctx, "dispatch", func(ctx context.Context) error {
		planner := gimble.NewSession(ctx, "planner", &exampleLoopAdapter{}, "example", ".")
		loop := gimble.Loop(ctx, "work", "demonstrate adaptive dispatch", planner)
		for ctx, task := range loop.Tasks {
			fmt.Println(task.Name)
			if err := gimble.Set(ctx, "result", "assignment recorded"); err != nil {
				return err
			}
		}
		return loop.Err()
	})
	fmt.Println(err)

	// Output:
	// Show the task
	// <nil>
}
