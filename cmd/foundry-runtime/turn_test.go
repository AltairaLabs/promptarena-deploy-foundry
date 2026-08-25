package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
	"github.com/AltairaLabs/PromptKit/runtime/statestore/file"
	"github.com/AltairaLabs/PromptKit/sdk"
)

// One Foundry agent serves the whole pack, so which member answers is the
// pack's decision unless the deployment pins one.
func TestResolveAgentName(t *testing.T) {
	tests := []struct {
		name string
		cfg  *runtimeConfig
		pack *prompt.Pack
		want string
	}{
		{
			name: "an explicit agent wins",
			cfg:  &runtimeConfig{AgentName: "pinned"},
			pack: &prompt.Pack{Agents: &prompt.AgentsConfig{Entry: "entry"}},
			want: "pinned",
		},
		{
			name: "otherwise the pack's entry",
			cfg:  &runtimeConfig{},
			pack: &prompt.Pack{Agents: &prompt.AgentsConfig{Entry: "entry"}},
			want: "entry",
		},
		{
			name: "a single prompt needs no entry",
			cfg:  &runtimeConfig{},
			pack: &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{"solo": {}}},
			want: "solo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAgentName(tt.cfg, tt.pack)
			if err != nil {
				t.Fatalf("resolveAgentName: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveAgentName = %q, want %q", got, tt.want)
			}
		})
	}
}

// Ambiguity must fail at startup rather than pick arbitrarily and serve the
// wrong agent for the life of the deployment.
func TestResolveAgentNameAmbiguous(t *testing.T) {
	pack := &prompt.Pack{Prompts: map[string]*prompt.PackPrompt{"a": {}, "b": {}}}

	if _, err := resolveAgentName(&runtimeConfig{}, pack); err == nil {
		t.Fatal("resolveAgentName picked one of several prompts")
	}
}

// The conversation id is transport only: it maps to a PromptKit session key so
// the pack stays authoritative over its own history.
func TestConversationOptionsAddsTheConversationID(t *testing.T) {
	base := []sdk.Option{}

	got := conversationOptions(base, &invocationRequest{ConversationID: "c-1"}, nil, "")
	if len(got) != 1 {
		t.Errorf("len(opts) = %d, want the conversation id added", len(got))
	}
}

func TestConversationOptionsWithoutAConversationID(t *testing.T) {
	base := []sdk.Option{}

	if got := conversationOptions(base, &invocationRequest{}, nil, ""); len(got) != 0 {
		t.Errorf("len(opts) = %d, want the base unchanged", len(got))
	}
}

// The base slice must not be mutated: it is shared by every turn, and
// appending in place would leak one request's id into the next.
func TestConversationOptionsDoesNotMutateTheBase(t *testing.T) {
	base := make([]sdk.Option, 0, 4)
	base = append(base, sdk.WithUserID("t"))

	_ = conversationOptions(base, &invocationRequest{ConversationID: "c-1"}, nil, "")

	if len(base) != 1 {
		t.Errorf("len(base) = %d, want it unchanged", len(base))
	}
}

// A bare "404 Resource not found" is indistinguishable between a wrong
// endpoint, a wrong deployment and an identity without access.
func TestBindingDescription(t *testing.T) {
	setBindingDescription("https://ep/openai/v1", "openai", "gpt-4-1-mini", "client-123")

	got := describeBinding()
	for _, want := range []string{"https://ep/openai/v1", "openai", "gpt-4-1-mini", "client-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeBinding() = %q, want it to name %q", got, want)
		}
	}
}

// An absent identity is the interesting case, so it is named rather than
// rendered as an empty string.
func TestBindingDescriptionWithoutAnIdentity(t *testing.T) {
	setBindingDescription("https://ep", "openai", "m", "")

	if got := describeBinding(); !strings.Contains(got, "no client id") {
		t.Errorf("describeBinding() = %q, want it to flag the missing identity", got)
	}
}

// warnUnsupportedTools reports once per process; calling it must be safe and
// silent when everything is supported.
func TestWarnUnsupportedToolsWithNothingUnsupported(t *testing.T) {
	warnUnsupportedTools(nil)
}

// fakeTurnConversation replays canned output for one turn.
type fakeTurnConversation struct {
	reply   string
	askErr  error
	chunks  []sdk.StreamChunk
	closed  bool
	gotText string
}

func (f *fakeTurnConversation) Ask(_ context.Context, message string) (string, error) {
	f.gotText = message
	return f.reply, f.askErr
}

func (f *fakeTurnConversation) StreamAsk(_ context.Context, message string) <-chan sdk.StreamChunk {
	f.gotText = message
	ch := make(chan sdk.StreamChunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return ch
}

func (f *fakeTurnConversation) Close() error {
	f.closed = true
	return nil
}

func openerFor(conv turnConversation, err error) turnOpener {
	return func(*invocationRequest) (turnConversation, error) {
		if err != nil {
			return nil, err
		}
		return conv, nil
	}
}

func TestNewTurnFuncReturnsTheReply(t *testing.T) {
	conv := &fakeTurnConversation{reply: "Paris"}

	got, err := newTurnFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "capital?"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got != "Paris" {
		t.Errorf("reply = %q, want Paris", got)
	}
	if conv.gotText != "capital?" {
		t.Errorf("sent %q, want the request's message", conv.gotText)
	}
}

// Every turn opens its own conversation, so every turn must close it or a
// long-lived session leaks one per request.
func TestNewTurnFuncClosesTheConversation(t *testing.T) {
	conv := &fakeTurnConversation{reply: "x"}

	if _, err := newTurnFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "hi"}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !conv.closed {
		t.Error("conversation was not closed")
	}
}

// A failed turn must name the binding, because Azure's own message does not.
func TestNewTurnFuncNamesTheBindingOnFailure(t *testing.T) {
	setBindingDescription("https://ep", "openai", "dep", "id")
	conv := &fakeTurnConversation{askErr: errors.New("404")}

	_, err := newTurnFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "hi"})
	if err == nil {
		t.Fatal("turn succeeded despite a send failure")
	}
	if !strings.Contains(err.Error(), "dep") {
		t.Errorf("err = %v, want it to name the deployment", err)
	}
}

func TestNewTurnFuncReportsAnOpenFailure(t *testing.T) {
	_, err := newTurnFunc(openerFor(nil, errors.New("no pack")))(
		context.Background(), &invocationRequest{Message: "hi"})
	if err == nil {
		t.Fatal("turn succeeded despite an open failure")
	}
}

func TestNewStreamFuncEmitsTextChunks(t *testing.T) {
	conv := &fakeTurnConversation{chunks: []sdk.StreamChunk{
		{Type: sdk.ChunkText, Text: "Hel"},
		{Type: sdk.ChunkText, Text: "lo"},
	}}

	chunks, errs := newStreamFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "hi"})

	var got string
	for c := range chunks {
		got += c
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got != "Hello" {
		t.Errorf("streamed %q, want Hello", got)
	}
}

// Only text reaches the caller: tool and empty chunks are pipeline detail the
// invocations contract does not carry.
func TestNewStreamFuncSkipsNonTextChunks(t *testing.T) {
	conv := &fakeTurnConversation{chunks: []sdk.StreamChunk{
		{Type: sdk.ChunkToolCall},
		{Type: sdk.ChunkText, Text: ""},
		{Type: sdk.ChunkText, Text: "only"},
	}}

	chunks, _ := newStreamFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "hi"})

	var got string
	for c := range chunks {
		got += c
	}
	if got != "only" {
		t.Errorf("streamed %q, want only", got)
	}
}

func TestNewStreamFuncPropagatesAChunkError(t *testing.T) {
	conv := &fakeTurnConversation{chunks: []sdk.StreamChunk{
		{Type: sdk.ChunkText, Text: "partial"},
		{Error: errors.New("died midway")},
	}}

	chunks, errs := newStreamFunc(openerFor(conv, nil))(
		context.Background(), &invocationRequest{Message: "hi"})
	for range chunks { //nolint:revive // draining is the point
	}

	if err := <-errs; err == nil {
		t.Fatal("stream reported success despite a chunk error")
	}
}

func TestNewStreamFuncReportsAnOpenFailure(t *testing.T) {
	chunks, errs := newStreamFunc(openerFor(nil, errors.New("no pack")))(
		context.Background(), &invocationRequest{Message: "hi"})
	for range chunks { //nolint:revive // draining is the point
	}

	if err := <-errs; err == nil {
		t.Fatal("stream reported success despite an open failure")
	}
}

// A caller that binds its turns to a session but sends no conversation id
// still gets continuity: the sandbox's own id keys the history. Without this
// the only way to be remembered was to invent an id, which the platform's own
// session binding already does.
func TestConversationOptionsFallsBackToTheSessionID(t *testing.T) {
	if got := conversationKey(&invocationRequest{}, "sess-9"); got != "sess-9" {
		t.Errorf("conversationKey = %q, want the session id", got)
	}
}

// An explicit conversation id wins, so one sandbox can carry more than one
// conversation.
func TestConversationKeyPrefersTheConversationID(t *testing.T) {
	got := conversationKey(&invocationRequest{ConversationID: "c-1"}, "sess-9")
	if got != "c-1" {
		t.Errorf("conversationKey = %q, want the conversation id", got)
	}
}

// Neither id means the caller asked for no continuity, and attaching a store
// keyed by nothing would let unrelated callers read each other's turns.
func TestConversationKeyEmptyWhenNeitherIsSet(t *testing.T) {
	if got := conversationKey(&invocationRequest{}, ""); got != "" {
		t.Errorf("conversationKey = %q, want empty", got)
	}
}

// The store is attached whenever there is a key to file history under.
func TestConversationOptionsAttachesTheStore(t *testing.T) {
	store, err := file.NewStore(file.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got := conversationOptions(nil, &invocationRequest{}, store, "sess-9")
	if len(got) != 2 {
		t.Errorf("len(opts) = %d, want the conversation id and the store", len(got))
	}
}

// A stateless turn must not get the store, or its history would be filed
// under whatever key the SDK defaulted to.
func TestConversationOptionsSkipsTheStoreWithoutAKey(t *testing.T) {
	store, err := file.NewStore(file.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if got := conversationOptions(nil, &invocationRequest{}, store, ""); len(got) != 0 {
		t.Errorf("len(opts) = %d, want none", len(got))
	}
}

// A pack the runtime cannot open has to surface as an error on the turn rather
// than a conversation that answers from nothing.
func TestNewSDKOpenerReportsAnUnopenablePack(t *testing.T) {
	open := newSDKOpener(
		filepath.Join(t.TempDir(), "missing.json"), "main", nil, nil, nil, "")

	if _, err := open(&invocationRequest{ConversationID: "c-1"}); err == nil {
		t.Fatal("opener succeeded on a pack that does not exist")
	}
}

// Without a store there is nothing to resume, so the opener must still work --
// this is the stateless path, and it must not require a store to exist.
func TestOpenOrResumeWithoutAStoreStillOpens(t *testing.T) {
	_, err := openOrResume(
		filepath.Join(t.TempDir(), "missing.json"), "main", "c-1", nil, nil)
	if err == nil {
		t.Fatal("openOrResume succeeded on a missing pack")
	}
	// A missing pack must fail as a pack error, not as a resume error: the
	// store path was never meant to be consulted here.
	if errors.Is(err, sdk.ErrConversationNotFound) {
		t.Error("openOrResume reported a missing conversation for a missing pack")
	}
}

// A conversation nobody has spoken to yet is the first turn, not a failure.
// Resume reports ErrConversationNotFound for it, and the opener has to treat
// that as "start one" or no conversation could ever begin.
func TestOpenOrResumeStartsAFirstTurn(t *testing.T) {
	store, err := file.NewStore(file.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	_, openErr := openOrResume(
		filepath.Join(t.TempDir(), "missing.json"), "main", "never-seen", store, nil)

	// The pack is missing so this cannot succeed, but it must have fallen
	// through to Open rather than stopping at the absent conversation.
	if errors.Is(openErr, sdk.ErrConversationNotFound) {
		t.Error("openOrResume gave up on a conversation that had not started yet")
	}
}
