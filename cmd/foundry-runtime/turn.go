package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/AltairaLabs/PromptKit/runtime/statestore/file"
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

// conversationKey is the key one turn's history is stored under.
//
// The caller's conversation id wins, so one sandbox can carry more than one
// conversation. Falling back to the sandbox's own session id means a caller who
// binds turns to a session -- which is what the agent_session_id query
// parameter does, and the only thing that selects a sandbox -- gets continuity
// without also having to invent an id of its own.
//
// Empty means the caller asked for neither, and the turn stays stateless.
func conversationKey(req *invocationRequest, sessionID string) string {
	if req.ConversationID != "" {
		return req.ConversationID
	}
	return sessionID
}

// conversationOptions adds the per-request options for one turn.
//
// The platform manages history for `responses`; over `invocations` it stores
// none, so the pack stays authoritative and history is written to the store
// under the conversation key. Letting both sides own history would have them
// fight over the same responsibility.
//
// The store is attached only alongside a key. Without one there is nothing to
// file the history under, and a shared store keyed by nothing would let
// unrelated callers read each other's turns.
func conversationOptions(
	base []sdk.Option, req *invocationRequest, store *file.Store, sessionID string,
) []sdk.Option {
	key := conversationKey(req, sessionID)
	if key == "" {
		return base
	}

	// The conversation id and the store.
	const added = 2
	opts := make([]sdk.Option, 0, len(base)+added)
	opts = append(opts, base...)
	opts = append(opts, sdk.WithConversationID(key))
	if store != nil {
		opts = append(opts, sdk.WithStateStore(store))
	}
	return opts
}

// turnConversation is the slice of a conversation one turn uses.
//
// It is narrowed to the reply text rather than exposing sdk.Response, whose
// fields are unexported and so cannot be constructed by a test. The runtime
// only ever wants the text.
type turnConversation interface {
	Ask(ctx context.Context, message string) (string, error)
	StreamAsk(ctx context.Context, message string) <-chan sdk.StreamChunk
	Close() error
}

// turnOpener starts a conversation for one request.
type turnOpener func(req *invocationRequest) (turnConversation, error)

// sdkConversation adapts a real conversation to turnConversation.
type sdkConversation struct{ conv *sdk.Conversation }

// Ask runs one unary turn and returns the reply text.
func (c sdkConversation) Ask(ctx context.Context, message string) (string, error) {
	resp, err := c.conv.Send(ctx, message)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}

// StreamAsk runs one streaming turn.
func (c sdkConversation) StreamAsk(ctx context.Context, message string) <-chan sdk.StreamChunk {
	return c.conv.Stream(ctx, message)
}

// Close releases the conversation.
func (c sdkConversation) Close() error { return c.conv.Close() }

// newSDKOpener opens a real conversation per request. A conversation per
// request keeps requests isolated, which matches Foundry's per-session sandbox
// model.
func newSDKOpener(
	packFile, agentName string, opts []sdk.Option, specs map[string]toolSpec,
	store *file.Store, sessionID string,
) turnOpener {
	return func(req *invocationRequest) (turnConversation, error) {
		conv, err := sdk.Open(packFile, agentName,
			conversationOptions(opts, req, store, sessionID)...)
		if err != nil {
			return nil, fmt.Errorf("open conversation: %w", err)
		}
		warnUnsupportedTools(registerToolExecutors(conv, specs))
		return sdkConversation{conv: conv}, nil
	}
}

// newTurnFunc returns a turnFunc that runs one unary turn per request.
func newTurnFunc(open turnOpener) turnFunc {
	return func(ctx context.Context, req *invocationRequest) (string, error) {
		conv, err := open(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = conv.Close() }()

		out, err := conv.Ask(ctx, req.text())
		if err != nil {
			// Name the binding that failed. A bare "404 Resource not found"
			// from Azure OpenAI is indistinguishable between a wrong endpoint,
			// a wrong deployment name, and an identity without access.
			return "", fmt.Errorf("send (%s): %w", describeBinding(), err)
		}
		return out, nil
	}
}

// newStreamFunc returns a streamFunc that streams one turn's text chunks.
func newStreamFunc(open turnOpener) streamFunc {
	return func(ctx context.Context, req *invocationRequest) (<-chan string, <-chan error) {
		out := make(chan string)
		errCh := make(chan error, 1)

		go func() {
			defer close(out)
			defer close(errCh)

			if err := streamTurn(ctx, open, req, out); err != nil {
				errCh <- err
			}
		}()

		return out, errCh
	}
}

// streamTurn runs one streaming turn, sending each text chunk to out.
func streamTurn(
	ctx context.Context, open turnOpener, req *invocationRequest, out chan<- string,
) error {
	conv, err := open(req)
	if err != nil {
		return err
	}
	defer func() { _ = conv.Close() }()

	for chunk := range conv.StreamAsk(ctx, req.text()) {
		if chunk.Error != nil {
			return chunk.Error
		}
		// Only text reaches the caller; tool and media chunks are pipeline
		// detail the invocations contract does not carry.
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

// bindingDescription is set at startup so a turn failure can report which
// provider configuration produced it.
var bindingDescription = "unconfigured"

// describeBinding returns the resolved provider configuration for error text.
func describeBinding() string { return bindingDescription }

// setBindingDescription records the resolved configuration for diagnostics.
func setBindingDescription(endpoint, providerType, model, clientID string) {
	id := clientID
	if id == "" {
		id = "(no client id injected)"
	}
	bindingDescription = fmt.Sprintf(
		"endpoint=%s type=%s deployment=%s identity=%s", endpoint, providerType, model, id)
}
