package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// packFileName is the local filename the resolved pack is written to.
const packFileName = "pack.json"

// packFilePerm is the permission mode for the written pack file.
const packFilePerm = 0o600

// packDirPerm is the permission mode for a directory created to hold it.
const packDirPerm = 0o700

// Writable locations Foundry provides to a session's sandbox. The rest of the
// container's filesystem is read-only — reproduced locally, where a read-only
// root made the process die at startup with "read-only file system" and never
// answer /readiness.
//
// These are named explicitly rather than trusted to arrive through $HOME,
// because $HOME is not reliably set: the same design note that promised it also
// claimed /readiness gated deployment, which measurement disproved.
const (
	sessionHomeDir  = "/home/session"
	sessionFilesDir = "/files"
)

// packDirCandidates lists where to try writing the pack, best first.
//
// $HOME comes first because it is the location Foundry documents as writable
// and persisted for the session's sandbox. The rest are fallbacks: the sandbox
// makes no promise about the rest of the filesystem, and a container that
// cannot write its pack exits before answering /readiness — which the platform
// surfaces only as an opaque "session did not become ready", with container
// startup logs not exposed through any API.
func packDirCandidates(getenv func(string) string) []string {
	var dirs []string
	if home := getenv("HOME"); home != "" {
		dirs = append(dirs, home)
	}
	return append(dirs, sessionHomeDir, sessionFilesDir, os.TempDir(), ".")
}

// resolvePackFile materializes the pack as a local file and returns its path,
// trying each candidate directory until one accepts the write.
//
// Only an inline pack is supported so far. Blob staging is unimplemented on
// both sides, so a URI fails loudly rather than starting a container that
// cannot load the pack it was deployed with.
func resolvePackFile(cfg *runtimeConfig, dirs []string) (string, error) {
	if cfg.PackJSON == "" {
		return "", fmt.Errorf(
			"%s is set to %q but staged packs are not supported yet; "+
				"reduce the pack below the inline limit",
			envPackURI, cfg.PackURI)
	}

	var lastErr error
	for _, dir := range dirs {
		// Create the directory first: a writable mount may be present with no
		// subdirectory, and skipping it would kill startup for no reason.
		if err := os.MkdirAll(dir, packDirPerm); err != nil {
			lastErr = err
			continue
		}
		path := filepath.Join(dir, packFileName)
		if err := os.WriteFile(path, []byte(cfg.PackJSON), packFilePerm); err != nil {
			lastErr = err
			continue
		}
		return path, nil
	}

	return "", fmt.Errorf("write pack file to any of %v: %w", dirs, lastErr)
}
