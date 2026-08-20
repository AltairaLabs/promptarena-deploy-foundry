package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/AltairaLabs/PromptKit/sdk"
)

// warnOnce ensures unsupported tool modes are reported once per process rather
// than on every request.
var warnOnce sync.Once

// warnUnsupportedTools logs tool modes this runtime cannot execute. A tool that
// looks configured but never runs is exactly the failure this path prevents, so
// it must be visible.
func warnUnsupportedTools(unsupported []string) {
	if len(unsupported) == 0 {
		return
	}
	warnOnce.Do(func() {
		slog.Warn("tools declared with unsupported execution modes will not run",
			"tools", strings.Join(unsupported, ", "))
	})
}

// conversationOptions adds the per-request options for one turn.
//
// The platform manages history for `responses`; over `invocations` the pack
// stays authoritative, so a conversation id is mapped to a PromptKit session
// key and nothing more. Letting both sides own history would have them fight
// over the same responsibility.
func conversationOptions(base []sdk.Option, req *invocationRequest) []sdk.Option {
	if req.ConversationID == "" {
		return base
	}
	opts := make([]sdk.Option, 0, len(base)+1)
	opts = append(opts, base...)
	return append(opts, sdk.WithConversationID(req.ConversationID))
}

// newTurnFunc returns a turnFunc that opens a fresh conversation per request.
// A conversation per request keeps requests isolated, which matches Foundry's
// per-session sandbox model.
func newTurnFunc(
	packFile, agentName string, opts []sdk.Option, specs map[string]toolSpec,
) turnFunc {
	return func(ctx context.Context, req *invocationRequest) (string, error) {
		conv, err := sdk.Open(packFile, agentName, conversationOptions(opts, req)...)
		if err != nil {
			return "", fmt.Errorf("open conversation: %w", err)
		}
		warnUnsupportedTools(registerToolExecutors(conv, specs))

		resp, err := conv.Send(ctx, req.text())
		if err != nil {
			return "", fmt.Errorf("send: %w", err)
		}
		return resp.Text(), nil
	}
}

// newStreamFunc returns a streamFunc that opens a fresh conversation per
// request and streams the turn's text chunks.
func newStreamFunc(
	packFile, agentName string, opts []sdk.Option, specs map[string]toolSpec,
) streamFunc {
	return func(ctx context.Context, req *invocationRequest) (<-chan string, <-chan error) {
		out := make(chan string)
		errCh := make(chan error, 1)

		go func() {
			defer close(out)
			defer close(errCh)

			if err := streamTurn(ctx, packFile, agentName, opts, specs, req, out); err != nil {
				errCh <- err
			}
		}()

		return out, errCh
	}
}

// streamTurn runs one streaming turn, sending each text chunk to out.
func streamTurn(
	ctx context.Context, packFile, agentName string,
	opts []sdk.Option, specs map[string]toolSpec,
	req *invocationRequest, out chan<- string,
) error {
	conv, err := sdk.Open(packFile, agentName, conversationOptions(opts, req)...)
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}
	warnUnsupportedTools(registerToolExecutors(conv, specs))

	for chunk := range conv.Stream(ctx, req.text()) {
		if chunk.Error != nil {
			return chunk.Error
		}
		if chunk.Type != sdk.ChunkText || chunk.Text == "" {
			continue
		}
		select {
		case out <- chunk.Text:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
