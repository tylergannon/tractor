package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	claudeagent "github.com/roasbeef/claude-agent-sdk-go"
)

type sdkSession struct {
	client *claudeagent.Client
	stream *claudeagent.Stream
	errors chan error
	cancel context.CancelFunc
}

func openNativeSession(ctx context.Context, config nativeConfig) (nativeSession, error) {
	extraArgs := make(map[string]*string)
	nativeErrors := make(chan error, 1)
	options := []claudeagent.Option{
		claudeagent.WithCwd(config.workdir),
		claudeagent.WithPermissionMode(claudeagent.PermissionModeBypassAll),
		claudeagent.WithAllowDangerouslySkipPermissions(true),
	}
	if config.model != "" {
		options = append(options, claudeagent.WithModel(config.model))
	}
	if config.reasoningEffort != "" {
		options = append(options, claudeagent.WithEffort(claudeagent.EffortLevel(config.reasoningEffort)))
	}
	if config.outputSchema != nil {
		rawSchema, err := json.Marshal(config.outputSchema)
		if err != nil {
			return nil, fmt.Errorf("encode Claude output schema: %w", err)
		}
		schema := string(rawSchema)
		extraArgs["json-schema"] = &schema
	}
	if config.fresh {
		sessionID := config.sessionID
		options = append(options,
			claudeagent.WithSessionOptions(claudeagent.SessionOptions{SessionID: config.sessionID}),
		)
		extraArgs["session-id"] = &sessionID
	} else {
		options = append(options, claudeagent.WithResume(config.sessionID))
	}
	if len(extraArgs) > 0 {
		options = append(options, claudeagent.WithExtraArgs(extraArgs))
	}
	options = append(options, claudeagent.WithStderr(func(data string) {
		if config.stderr != nil {
			_, _ = config.stderr.Write([]byte(data))
		}
		if nativeErr := fatalStderr(data); nativeErr != nil {
			select {
			case nativeErrors <- nativeErr:
			default:
			}
		}
	}))
	client, err := claudeagent.NewClient(options...)
	if err != nil {
		return nil, err
	}
	type streamResult struct {
		stream *claudeagent.Stream
		err    error
	}
	connectCtx, cancelConnect := context.WithCancel(ctx)
	connected := make(chan streamResult, 1)
	go func() {
		stream, streamErr := client.Stream(connectCtx)
		connected <- streamResult{stream: stream, err: streamErr}
	}()
	var stream *claudeagent.Stream
	select {
	case result := <-connected:
		stream, err = result.stream, result.err
	case nativeErr := <-nativeErrors:
		cancelConnect()
		<-connected
		err = nativeErr
	case <-ctx.Done():
		cancelConnect()
		<-connected
		err = ctx.Err()
	}
	if err != nil {
		cancelConnect()
		_ = client.Close()
		return nil, err
	}
	return &sdkSession{client: client, stream: stream, errors: nativeErrors, cancel: cancelConnect}, nil
}

func (s *sdkSession) Send(ctx context.Context, prompt string) error {
	return s.stream.Send(ctx, prompt)
}

func (s *sdkSession) Messages() iter.Seq[claudeagent.Message] {
	return s.stream.Messages()
}

func (s *sdkSession) Errors() <-chan error {
	return s.errors
}

func (s *sdkSession) InterruptWithReceipt(ctx context.Context) (*claudeagent.InterruptReceipt, error) {
	return s.stream.InterruptWithReceipt(ctx)
}

func (s *sdkSession) Close() error {
	s.cancel()
	_ = s.stream.Close()
	return s.client.Close()
}

func fatalStderr(data string) error {
	lower := strings.ToLower(data)
	for _, marker := range []string{"no conversation found", "invalid model", "unknown model"} {
		if strings.Contains(lower, marker) {
			return errors.New(strings.TrimSpace(data))
		}
	}
	return nil
}

func normalizeNative(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return value
	}
	return decoded
}

func asResult(message claudeagent.Message) (claudeagent.ResultMessage, bool) {
	switch result := message.(type) {
	case claudeagent.ResultMessage:
		return result, true
	case *claudeagent.ResultMessage:
		return *result, true
	default:
		return claudeagent.ResultMessage{}, false
	}
}

func asStatus(message claudeagent.Message) (claudeagent.StatusMessage, bool) {
	switch status := message.(type) {
	case claudeagent.StatusMessage:
		return status, true
	case *claudeagent.StatusMessage:
		return *status, true
	default:
		return claudeagent.StatusMessage{}, false
	}
}
