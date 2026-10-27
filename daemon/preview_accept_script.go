package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed previewassets/accept-preview.mjs
var previewAcceptScript []byte

func materializePreviewAcceptScript(dir string) (string, error) {
	path := filepath.Join(dir, "accept-preview.mjs")
	if err := os.WriteFile(path, previewAcceptScript, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
