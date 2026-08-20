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
	unwritable := readOnlyDir(t)
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
	bad := readOnlyDir(t)
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

// The sandbox root is read-only — reproduced locally, where the container died
// with "read-only file system" and never answered /readiness. The writable
// mounts are the session's own, so those paths are tried explicitly rather than
// trusted to arrive via $HOME.
func TestPackDirCandidatesIncludesSessionMounts(t *testing.T) {
	got := packDirCandidates(func(string) string { return "" })

	want := map[string]bool{sessionHomeDir: false, sessionFilesDir: false}
	for _, dir := range got {
		if _, ok := want[dir]; ok {
			want[dir] = true
		}
	}
	for dir, found := range want {
		if !found {
			t.Errorf("candidates = %v, want %q included", got, dir)
		}
	}
}

// A candidate that does not exist yet must be created rather than skipped: the
// mount may be present with no subdirectory, and giving up would kill startup.
func TestResolvePackFileCreatesTheDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "made", "up")
	cfg := &runtimeConfig{PackJSON: `{"id":"p"}`}

	path, err := resolvePackFile(cfg, []string{target})
	if err != nil {
		t.Fatalf("resolvePackFile: %v", err)
	}
	if filepath.Dir(path) != target {
		t.Errorf("wrote to %q, want it under %q", path, target)
	}
}

// readOnlyDir returns a directory that exists but cannot be written to, which
// is what the sandbox's read-only root behaves like. A merely absent directory
// is no longer a stand-in for this: resolvePackFile creates missing ones on
// purpose.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("create read-only dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("running as root, which bypasses directory permissions")
	}
	return dir
}
