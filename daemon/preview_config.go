package main

import (
	"fmt"
	"net/url"

	"path"
	"path/filepath"
	"regexp"

	"sort"
	"strings"

	"unicode"
)

type repoPreviewConfig struct {
	AppDir      string            `json:"appDir"`
	Kind        string            `json:"kind"`
	BuildArgv   []string          `json:"buildArgv,omitempty"`
	BuildDir    string            `json:"buildDir,omitempty"`
	StaticDir   string            `json:"staticDir,omitempty"`
	ServeArgv   []string          `json:"serveArgv,omitempty"`
	HealthPath  string            `json:"healthPath,omitempty"`
	SpaFallback bool              `json:"spaFallback,omitempty"`
	PublicBase  string            `json:"publicBase,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

var previewEnvKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func validatePreviewRelativePath(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a clean relative path", name)
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if path.Clean(normalized) != normalized || strings.HasPrefix(normalized, "//") || (len(normalized) >= 2 && normalized[1] == ':') {
		return fmt.Errorf("%s must not be absolute", name)
	}
	for _, segment := range strings.FieldsFunc(normalized, func(r rune) bool { return r == '/' }) {
		if segment == ".." {
			return fmt.Errorf("%s must not contain '..'", name)
		}
	}
	return nil
}

// isBareExecutableName accepts only a name resolvable through PATH. A value
// carrying a separator would be a filesystem lookup relative to the daemon's
// working directory, not the worktree — declare those with `path` instead.
func isBareExecutableName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return r == '/' || r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

func validatePreviewEnv(env map[string]string) error {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		upper := strings.ToUpper(key)
		if !previewEnvKeyRE.MatchString(key) {
			return fmt.Errorf("env key %q must match [A-Z][A-Z0-9_]*", key)
		}
		if upper == "PATH" || upper == "HOME" || upper == "NODE_OPTIONS" || strings.HasPrefix(upper, "SOLU_") || strings.HasPrefix(upper, "PLAYWRIGHT_") {
			return fmt.Errorf("env key %q is not allowed in a preview", key)
		}
		if len(env[key]) > 4<<10 {
			return fmt.Errorf("env value %q exceeds 4096 bytes", key)
		}
	}
	return nil
}

func validatePreviewHealthPath(value string) error {
	if value == "" || !strings.HasPrefix(value, "/") {
		return fmt.Errorf("healthPath must begin with '/'")
	}
	if strings.HasPrefix(value, "//") || strings.Contains(value, "://") || strings.ContainsAny(value, "?#\\") {
		return fmt.Errorf("healthPath must not contain a host, query, or fragment")
	}
	if parsed, err := url.Parse(value); err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("healthPath must be a path only")
	}
	return nil
}

func previewArgvUsesShell(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	switch base {
	case "sh", "bash", "dash", "zsh", "ksh":
		for _, arg := range argv[1:] {
			if strings.EqualFold(arg, "--command") || strings.HasPrefix(arg, "-") && strings.Contains(strings.TrimPrefix(arg, "-"), "c") {
				return true
			}
		}
	case "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		for _, arg := range argv[1:] {
			if strings.EqualFold(arg, "-c") || strings.EqualFold(arg, "-command") || strings.EqualFold(arg, "/c") {
				return true
			}
		}
	}
	return false
}

func validatePreviewArgv(name string, argv []string, required bool) error {
	if len(argv) == 0 {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("%s[0] must be non-empty", name)
	}
	if strings.IndexFunc(argv[0], func(r rune) bool { return r == 0 || r == '\r' || r == '\n' || r == '\t' }) >= 0 || strings.Contains(argv[0], " ") && !strings.ContainsAny(argv[0], `/\`) {
		return fmt.Errorf("%s[0] must be an executable name, not a shell command string", name)
	}
	if previewArgvUsesShell(argv) {
		return fmt.Errorf("%s must be an argv command, not a shell string", name)
	}
	return nil
}

func validateRepoPreviewConfig(preview *repoPreviewConfig) error {
	if preview == nil {
		return nil
	}
	if preview.AppDir == "" {
		preview.AppDir = "."
	}
	if preview.HealthPath == "" {
		preview.HealthPath = "/"
	}
	if preview.PublicBase == "" {
		preview.PublicBase = "relative"
	}
	if err := validatePreviewRelativePath("preview.appDir", preview.AppDir, true); err != nil {
		return err
	}
	if err := validatePreviewArgv("preview.buildArgv", preview.BuildArgv, false); err != nil {
		return err
	}
	if err := validatePreviewHealthPath(preview.HealthPath); err != nil {
		return err
	}
	if err := validatePreviewEnv(preview.Env); err != nil {
		return fmt.Errorf("preview.%w", err)
	}
	switch preview.Kind {
	case previewModeStatic:
		if preview.BuildDir != "" {
			return fmt.Errorf("preview.buildDir is valid only for kind spawn")
		}
		if err := validatePreviewRelativePath("preview.staticDir", preview.StaticDir, true); err != nil {
			return err
		}
		if len(preview.ServeArgv) != 0 {
			return fmt.Errorf("preview.serveArgv is valid only for kind spawn")
		}
		if preview.PublicBase != "relative" {
			return fmt.Errorf("preview.publicBase must be relative for kind static")
		}
	case previewModeSpawn:
		if preview.StaticDir != "" {
			return fmt.Errorf("preview.staticDir is valid only for kind static")
		}
		if err := validatePreviewRelativePath("preview.buildDir", preview.BuildDir, false); err != nil {
			return err
		}
		if err := validatePreviewArgv("preview.serveArgv", preview.ServeArgv, true); err != nil {
			return err
		}
		if preview.PublicBase != "relative" && preview.PublicBase != "basePath" {
			return fmt.Errorf("preview.publicBase must be relative or basePath")
		}
		if preview.PublicBase == "basePath" {
			if err := validatePreviewArgv("preview.buildArgv", preview.BuildArgv, true); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("preview.kind must be static or spawn")
	}
	return nil
}

func cloneRepoPreviewConfig(preview *repoPreviewConfig) *repoPreviewConfig {
	if preview == nil {
		return nil
	}
	copy := *preview
	copy.BuildArgv = append([]string(nil), preview.BuildArgv...)
	copy.ServeArgv = append([]string(nil), preview.ServeArgv...)
	copy.Env = clonePreviewEnv(preview.Env)
	return &copy
}
