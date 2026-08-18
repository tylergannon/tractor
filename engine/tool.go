package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

var (
	errToolStopped  = errors.New("tool command stopped")
	errToolTimedOut = errors.New("tool command timed out")
)

func toolHandler(node graph.Node, offered []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
	tool, ok := node.(*graph.ToolNode)
	if !ok {
		return harness.Outcome{}, terminalError("tool handler received a non-tool node")
	}
	if strings.TrimSpace(tool.ToolCommand) == "" {
		return harness.Outcome{}, terminalError("no tool_command on " + tool.ID)
	}
	if scope.Stop != nil && scope.Stop.IsSet() {
		return harness.Outcome{}, interruptedError("tool command stopped by operator")
	}

	timeout, timeoutErr := toolTimeout(tool)
	if timeoutErr != nil {
		return harness.Outcome{}, terminalError(timeoutErr.Error())
	}
	logPath := filepath.Join(scope.StageDir, "tool.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("open tool log: %v", err))
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	commandDone := make(chan struct{})
	if scope.Stop != nil {
		go func() {
			select {
			case <-scope.Stop.done:
				cancel(errToolStopped)
			case <-commandDone:
			}
		}()
	}
	var timer *time.Timer
	if tool.Timeout.Present {
		timer = time.AfterFunc(timeout, func() { cancel(errToolTimedOut) })
	}

	command := exec.CommandContext(ctx, "/bin/sh", "-c", tool.ToolCommand)
	command.Dir = scope.Workdir
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	runErr := command.Run()
	close(commandDone)
	if timer != nil {
		timer.Stop()
	}
	cause := context.Cause(ctx)
	cancel(nil)
	closeErr := logFile.Close()

	switch {
	case errors.Is(cause, errToolStopped):
		return harness.Outcome{}, interruptedError("tool command stopped by operator")
	case errors.Is(cause, errToolTimedOut):
		return harness.Outcome{}, interruptedError("tool command timed out")
	case closeErr != nil:
		return harness.Outcome{}, terminalError(fmt.Sprintf("close tool log: %v", closeErr))
	}

	exitCode := 0
	if runErr != nil {
		var exitError *exec.ExitError
		if !errors.As(runErr, &exitError) {
			return harness.Outcome{}, terminalError(fmt.Sprintf("run tool command: %v", runErr))
		}
		exitCode = exitError.ExitCode()
	}

	route, routeErr := toolRoute(tool, exitCode)
	if routeErr != nil {
		return harness.Outcome{}, routeErr
	}
	if !offeredTarget(offered, route) {
		return harness.Outcome{}, terminalError(fmt.Sprintf("exit-code route %s has exhausted its visit budget", route))
	}
	tail, err := toolLogTail(logPath)
	if err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("read tool log: %v", err))
	}
	return harness.Outcome{
		Next:  route,
		Notes: fmt.Sprintf("exit %d: %s", exitCode, tail),
	}, nil
}

func toolTimeout(node *graph.ToolNode) (time.Duration, error) {
	if !node.Timeout.Present {
		return 0, nil
	}
	timeout, err := node.Timeout.Value.Parse()
	if err != nil {
		return 0, fmt.Errorf("parse tool timeout: %w", err)
	}
	return timeout, nil
}

func toolRoute(node *graph.ToolNode, exitCode int) (string, *harness.Error) {
	if exitCode != 0 {
		if !node.OnError.Present {
			return "", terminalError(fmt.Sprintf("%s exited %d", node.ToolCommand, exitCode))
		}
		return node.OnError.Value, nil
	}
	return node.OnSuccess, nil
}

func offeredTarget(offered []graph.Edge, target string) bool {
	for _, edge := range offered {
		if edge.To == target {
			return true
		}
	}
	return false
}

func toolLogTail(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	const maxTailRunes = 200
	tail := []rune(strings.TrimSpace(string(contents)))
	if len(tail) > maxTailRunes {
		tail = tail[len(tail)-maxTailRunes:]
	}
	return string(tail), nil
}
