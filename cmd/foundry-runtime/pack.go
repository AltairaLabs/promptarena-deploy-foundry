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

// resolvePackFile materializes the pack as a local file in dir and returns its
// path.
//
// Only an inline pack is supported so far. Blob staging is not implemented on
// either side, so a URI fails loudly here rather than starting a container that
// cannot load the pack it was deployed with.
func resolvePackFile(cfg *runtimeConfig, dir string) (string, error) {
	if cfg.PackJSON == "" {
		return "", fmt.Errorf(
			"%s is set to %q but staged packs are not supported yet; "+
				"reduce the pack below the inline limit",
			envPackURI, cfg.PackURI)
	}

	path := filepath.Join(dir, packFileName)
	if err := os.WriteFile(path, []byte(cfg.PackJSON), packFilePerm); err != nil {
		return "", fmt.Errorf("write pack file: %w", err)
	}
	return path, nil
}
