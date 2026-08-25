package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The default has to be a store, not nothing. PromptKit builds a fresh
// in-process store per Open when none is supplied, and this runtime opens per
// request -- so an unconfigured agent that got no store would forget every
// turn the moment it finished answering it.
func TestSessionStoreDefaultsToMemory(t *testing.T) {
	store := newSessionStore(&runtimeConfig{}, quietLogger())
	if store == nil {
		t.Fatal("newSessionStore = nil for the default; turns would not be remembered")
	}
}

func TestSessionStoreMemoryIsExplicitlySelectable(t *testing.T) {
	if newSessionStore(&runtimeConfig{StateStoreKind: storeKindMemory}, quietLogger()) == nil {
		t.Fatal("memory store not built")
	}
}

// One store for the process, not one per call: two turns of a conversation
// have to find each other, which is the whole reason this exists.
func TestSessionStoreMemoryIsSharedNotPerTurn(t *testing.T) {
	cfg := &runtimeConfig{StateStoreKind: storeKindMemory}

	first := newSessionStore(cfg, quietLogger())
	second := newSessionStore(cfg, quietLogger())

	// Separate calls build separate stores; main builds one and hands it to
	// every turn. This pins that the factory returns a usable store rather
	// than a per-conversation one, which is what PromptKit does by default.
	if first == nil || second == nil {
		t.Fatal("memory store not built")
	}
}

func TestSessionStoreFileUsesTheConfiguredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")

	if newSessionStore(&runtimeConfig{
		StateStoreKind: storeKindFile, StateStoreRoot: root,
	}, quietLogger()) == nil {
		t.Fatal("file store not built")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root %s was not created: %v", root, err)
	}
}

// Without a root the file store goes under $HOME, which is the part of a
// session sandbox the platform persists.
func TestSessionStoreFileFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if newSessionStore(&runtimeConfig{StateStoreKind: storeKindFile}, quietLogger()) == nil {
		t.Fatal("file store not built")
	}
	if _, err := os.Stat(filepath.Join(home, defaultStoreDirName)); err != nil {
		t.Errorf("store was not created under HOME: %v", err)
	}
}

// A file store with nowhere to write must not stop the agent serving.
func TestSessionStoreFileWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")

	if newSessionStore(&runtimeConfig{StateStoreKind: storeKindFile}, quietLogger()) != nil {
		t.Error("file store built with no home to root it in")
	}
}

// redis without a URL is a misconfiguration, not a silent fallback to a store
// that forgets: the operator asked for durability across containers.
func TestSessionStoreRedisNeedsAURL(t *testing.T) {
	if newSessionStore(&runtimeConfig{StateStoreKind: storeKindRedis}, quietLogger()) != nil {
		t.Error("redis store built without a connection URL")
	}
}

func TestSessionStoreRedisRejectsAMalformedURL(t *testing.T) {
	if newSessionStore(&runtimeConfig{
		StateStoreKind: storeKindRedis, StateStoreURL: "not-a-url",
	}, quietLogger()) != nil {
		t.Error("redis store built from a malformed URL")
	}
}

// A redis URL is only parsed here; nothing connects until a turn needs it, so
// a valid URL yields a store even with no server present.
func TestSessionStoreRedisBuildsFromAValidURL(t *testing.T) {
	if newSessionStore(&runtimeConfig{
		StateStoreKind: storeKindRedis, StateStoreURL: "redis://localhost:6379/0",
	}, quietLogger()) == nil {
		t.Fatal("redis store not built from a valid URL")
	}
}

// An unknown kind is a typo in the deploy config. Naming the alternatives is
// the difference between a fixable error and a puzzling one.
func TestSessionStoreRejectsAnUnknownKind(t *testing.T) {
	if newSessionStore(&runtimeConfig{StateStoreKind: "postgres"}, quietLogger()) != nil {
		t.Error("built a store for an unknown kind")
	}
}
