// Run with go run ./ephemeral/attest/issue-149. Three real cheap-model
// sessions run concurrently in three child scopes, each taking two turns so
// the second turn resumes the conversation. The runtime serves the live page
// on -port and is held open after the run so the run page and the scopeUsage
// live query can be read.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tylergannon/gimble"
	"github.com/tylergannon/gimble/agy"
	"github.com/tylergannon/gimble/claude"
	"github.com/tylergannon/gimble/codex"
	"github.com/tylergannon/gimble/web"
	"golang.org/x/sync/errgroup"
)

type lane struct {
	scope   string
	adapter gimble.HarnessAdapter
	model   string
}

const (
	firstPrompt  = "In two sentences, what is the difference between latency and throughput? Answer from your own knowledge and use no tools."
	secondPrompt = "Now, in two sentences, name one case where improving one of them makes the other worse. Again use no tools."
)

func main() {
	port := flag.Int("port", 8099, "loopback TCP port for the web application")
	hold := flag.Duration("hold", 15*time.Minute, "how long to hold the server open after the run")
	flag.Parse()

	base, err := filepath.Abs("ephemeral/attest/issue-149")
	if err != nil {
		log.Fatal(err)
	}
	logs := filepath.Join(base, "logs")
	if err := os.RemoveAll(logs); err != nil {
		log.Fatal(err)
	}
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := web.NewRuntime(ctx, logs, web.WithPort(*port))
	if err != nil {
		log.Fatal(err)
	}
	go announce(ctx, logs, *port)

	lanes := []lane{
		{scope: "claude", adapter: claude.New(), model: "haiku"},
		{scope: "codex", adapter: codex.New(), model: "gpt-5.6-luna"},
		{scope: "agy", adapter: agy.New(), model: "gemini-3.8-flash-low"},
	}
	fmt.Println("Models: claude=haiku codex=gpt-5.6-luna agy=gemini-3.8-flash-low")

	start := time.Now()
	runErr := runtime.Run(ctx, "issue-149", func(ctx context.Context) error {
		group, groupCtx := errgroup.WithContext(ctx)
		for _, each := range lanes {
			group.Go(func() error {
				return gimble.Scope(groupCtx, each.scope, func(ctx context.Context) error {
					session := gimble.NewSession(ctx, each.scope, each.adapter, each.model, workspace)
					first, err := session.Generate[gimble.Text](ctx, firstPrompt)
					if err != nil {
						return fmt.Errorf("%s turn 1: %w", each.scope, err)
					}
					fmt.Printf("[%s turn 1] %s\n", each.scope, first)
					second, err := session.Generate[gimble.Text](ctx, secondPrompt)
					if err != nil {
						return fmt.Errorf("%s turn 2: %w", each.scope, err)
					}
					fmt.Printf("[%s turn 2] %s\n", each.scope, second)
					return nil
				})
			})
		}
		return group.Wait()
	})
	fmt.Printf("Run finished in %s: err=%v\n", time.Since(start).Round(time.Millisecond), runErr)

	fmt.Printf("Holding the server for %s; press Enter to stop early.\n", *hold)
	done := make(chan struct{})
	go func() {
		var line [1]byte
		for {
			n, err := os.Stdin.Read(line[:])
			if err != nil {
				return // no console: hold for the full duration
			}
			if n > 0 && line[0] == '\n' {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(*hold):
	case <-ctx.Done():
	}
	if runErr != nil {
		os.Exit(1)
	}
}

// announce prints the run id and its page URL as soon as the run directory
// appears, so the page can be opened while the run is still going.
func announce(ctx context.Context, logs string, port int) {
	for ctx.Err() == nil {
		entries, err := os.ReadDir(filepath.Join(logs, "runs"))
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					fmt.Printf("Run: %s\nPage: http://127.0.0.1:%d/runs/%s\n", entry.Name(), port, entry.Name())
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}
