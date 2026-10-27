package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"soluterminal-shared/typesafe"
)

func defaultDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("SOLU_DATA_DIR")); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".solu")
}

// Resolve exactly the existing TypeSafe credential. Never print the store or
// include its contents in parser errors. Offline modes never call this helper.
func apiKey(dataDir string) (string, error) {
	if key := strings.TrimSpace(os.Getenv(typesafe.EnvAPIKey)); key != "" {
		return key, nil
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "workflow_secrets.json"))
	if err != nil {
		return "", fmt.Errorf("TypeSafe credential unavailable; save it in Credentials or set TYPESAFE_API_KEY")
	}
	var store struct {
		Runtimes map[string]map[string]string `json:"runtimes"`
	}
	if json.Unmarshal(raw, &store) != nil {
		return "", fmt.Errorf("TypeSafe credential store is malformed")
	}
	key := strings.TrimSpace(store.Runtimes["typesafe-api"][typesafe.EnvAPIKey])
	if key == "" {
		return "", fmt.Errorf("TypeSafe credential is not configured")
	}
	return key, nil
}
