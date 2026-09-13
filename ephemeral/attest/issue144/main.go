// Run with go run ./ephemeral/attest/issue144. Luna plans and does three
// trivial file tasks in a temporary fixture while the web application serves
// the run, so the snapshot and the page can be observed mid-Loop and after
// an interrupt.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/tylergannon/gimble"
	"github.com/tylergannon/gimble/codex"
	"github.com/tylergannon/gimble/web"
)

func main() {
	port := flag.Int("port", 8080, "loopback TCP port for the web application")
	model := flag.String("model", "gpt-5.6-luna", "Codex model for the planner and the workers")
	flag.Parse()
	dir, err := os.MkdirTemp("", "gimble-issue144-")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Fixture directory:", dir)
	// An interrupt cancels the run only; the web application stays up a
	// little longer so the page can be observed after the cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	serverCtx, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	runtime, err := web.NewRuntime(serverCtx, filepath.Join(dir, ".gimble"), web.WithPort(*port))
	if err != nil {
		log.Fatal(err)
	}
	adapter := codex.New()
	err = runtime.Run(ctx, "issue144", func(ctx context.Context) error {
		if err := gimble.Set(ctx, "constraint", "Only create files inside the working directory. Each task creates exactly one file."); err != nil {
			return err
		}
		planner := gimble.NewSession(ctx, "planner", adapter, *model, dir)
		loop := gimble.Loop(ctx, "files", "The working directory contains a.txt, b.txt and c.txt, each holding its own file name. One file per task; end dispatch once all three exist.", planner)
		count := 0
		for taskCtx, task := range loop.Tasks {
			fmt.Println("Task:", task.Name)
			count++
			if count > 4 {
				break
			}
			worker := gimble.NewSession(taskCtx, "worker", adapter, *model, dir)
			result, workErr := worker.Generate[gimble.Text](taskCtx, "Complete this assignment.\n\n"+gimble.ScopeText(taskCtx))
			if err := gimble.Set(taskCtx, "worker result", string(result)); err != nil {
				return err
			}
			if workErr != nil {
				return workErr
			}
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if err := gimble.Set(taskCtx, "files", names); err != nil {
				return err
			}
		}
		return loop.Err()
	})
	fmt.Println("Run ended:", err)
	time.Sleep(30 * time.Second)
}
