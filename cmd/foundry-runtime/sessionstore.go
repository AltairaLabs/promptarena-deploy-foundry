package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/statestore/file"
)

// Session state lives on the sandbox filesystem.
//
// A Foundry session is an isolated sandbox whose filesystem the platform
// persists across turns and across idle periods, restoring it when the session
// is referenced again. Conversation history written there therefore survives
// the compute being deprovisioned, which an in-process store does not.
//
// This is only durable for callers that bind their turns to one session. The
// platform routes on the agent_session_id query parameter and nothing else --
// a body field does not select a sandbox -- so a client that omits it gets a
// fresh sandbox, and a fresh history, on every turn.
const (
	// sessionStoreDirName is the directory under the state root that holds
	// conversation state.
	sessionStoreDirName = "sessions"
	// sessionStateTTL sweeps conversation directories the platform would have
	// discarded anyway: a session lives at most 30 days.
	sessionStateTTL = 30 * 24 * time.Hour
)

// sessionStoreRoot is where conversation state is written.
//
// $HOME, because that is the part of the sandbox the platform persists. There
// is deliberately no override: a setting the adapter never injects is a knob
// nobody can reach.
func sessionStoreRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for session state: %w", err)
	}
	return filepath.Join(home, sessionStoreDirName), nil
}

// newSessionStore opens the conversation store.
//
// A store that cannot be opened is reported, not fatal: the agent still serves
// turns, it just cannot remember them. Refusing to start would trade a degraded
// deployment for no deployment at all.
func newSessionStore(log *slog.Logger) *file.Store {
	root, err := sessionStoreRoot()
	if err != nil {
		log.Warn("no durable session store; conversations will not survive a restart",
			"error", err)
		return nil
	}

	store, err := file.NewStore(file.Options{Root: root, TTL: sessionStateTTL})
	if err != nil {
		log.Warn("no durable session store; conversations will not survive a restart",
			"root", root, "error", err)
		return nil
	}

	log.Info("conversation state is durable", "root", root)
	return store
}
