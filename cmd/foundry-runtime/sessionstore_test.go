package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// State goes under $HOME because that is the part of the sandbox the platform
// persists across turns and idle periods. Writing anywhere else would look
// durable in a test and vanish the moment compute was reclaimed.
func TestSessionStoreRootIsUnderHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root, err := sessionStoreRoot()
	if err != nil {
		t.Fatalf("sessionStoreRoot: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if !strings.HasPrefix(root, home) {
		t.Errorf("root = %q, want it under %q", root, home)
	}
	if filepath.Base(root) != sessionStoreDirName {
		t.Errorf("root = %q, want it to end in %q", root, sessionStoreDirName)
	}
}

func TestNewSessionStoreOpensAStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if store := newSessionStore(quietLogger()); store == nil {
		t.Fatal("newSessionStore = nil, want a store")
	}
}

// A store that cannot be opened must not stop the agent serving. Losing
// memory is a degraded deployment; refusing to start is no deployment.
func TestNewSessionStoreSurvivesAnUnusableHome(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("HOME", file)

	if store := newSessionStore(quietLogger()); store != nil {
		t.Error("newSessionStore returned a store rooted at a file")
	}
}
