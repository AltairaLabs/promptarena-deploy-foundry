package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePackFileWritesInline(t *testing.T) {
	dir := t.TempDir()
	cfg := &runtimeConfig{PackJSON: `{"id":"p"}`}

	path, err := resolvePackFile(cfg, []string{dir})
	if err != nil {
		t.Fatalf("resolvePackFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != cfg.PackJSON {
		t.Errorf("wrote %q, want %q", data, cfg.PackJSON)
	}
}

// The sandbox's writable location is not guaranteed, and a container that
// cannot write its pack exits before it ever answers /readiness — which the
// platform reports only as an opaque "session did not become ready".
func TestResolvePackFileFallsBackToAWritableDir(t *testing.T) {
	unwritable := filepath.Join(t.TempDir(), "nope", "deeper")
	good := t.TempDir()
	cfg := &runtimeConfig{PackJSON: `{"id":"p"}`}

	path, err := resolvePackFile(cfg, []string{unwritable, good})
	if err != nil {
		t.Fatalf("resolvePackFile: %v", err)
	}
	if filepath.Dir(path) != good {
		t.Errorf("wrote to %q, want it under %q", path, good)
	}
}

func TestResolvePackFileReportsWhenNothingIsWritable(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "nope", "deeper")
	cfg := &runtimeConfig{PackJSON: `{"id":"p"}`}

	_, err := resolvePackFile(cfg, []string{bad})
	if err == nil {
		t.Fatal("resolvePackFile succeeded with no writable directory")
	}
}

// Blob staging is unimplemented on both sides, so a staged pack must fail
// loudly rather than start a container that cannot load what it was deployed
// with.
func TestResolvePackFileRejectsAStagedPack(t *testing.T) {
	cfg := &runtimeConfig{PackURI: "https://acct.blob.core.windows.net/pk/pack.json"}

	if _, err := resolvePackFile(cfg, []string{t.TempDir()}); err == nil {
		t.Fatal("resolvePackFile accepted a staged pack")
	}
}

// $HOME is the location Foundry documents as persisted and writable, so it is
// tried before anything else.
func TestPackDirCandidatesPrefersHome(t *testing.T) {
	got := packDirCandidates(func(k string) string {
		if k == "HOME" {
			return "/home/session"
		}
		return ""
	})

	if len(got) == 0 || got[0] != "/home/session" {
		t.Errorf("candidates = %v, want $HOME first", got)
	}
	if len(got) < 2 {
		t.Errorf("candidates = %v, want a fallback after $HOME", got)
	}
}

func TestPackDirCandidatesWithoutHome(t *testing.T) {
	got := packDirCandidates(func(string) string { return "" })

	if len(got) == 0 {
		t.Fatal("candidates is empty with no HOME set")
	}
}
