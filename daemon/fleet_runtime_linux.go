//go:build linux

package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Stamped by the shared release helper. Missing metadata is NOT compatibility.
// v1's source analysis is Linux/amd64; other architectures keep exact-inode
// classification until their build graph has a platform-specific fingerprint.
var paneRuntimeSourceLinux = ""

func validRuntimeDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

// Receipts are local installation metadata, produced only after comparing a
// legacy build's source closure to the new one. Unknown, malformed, or changed
// artifacts fail closed. Never infer a legacy receipt from its version marker.
func fleetReadLegacyRuntimeReceipts(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64*1024 {
		return nil
	}
	var receipt struct {
		Schema    int               `json:"schema"`
		Artifacts map[string]string `json:"artifacts"`
	}
	if json.Unmarshal(b, &receipt) != nil || receipt.Schema != 1 {
		return nil
	}
	for artifact, source := range receipt.Artifacts {
		if !validRuntimeDigest(artifact) || !validRuntimeDigest(source) {
			return nil
		}
	}
	return receipt.Artifacts
}

func fleetReadPaneRuntime(path string, legacy map[string]string) string {
	// Keep daemon-only parsing out of package initialization: agent processes
	// do not need these regular expressions at all.
	fleetRuntimeSourceFlag := regexp.MustCompile(`(?:^|\s)-X(?:=|\s+)main\.paneRuntimeSourceLinux=([0-9a-f]{64})(?:\s|$)`)
	fleetRuntimeMetadataFlag := regexp.MustCompile(`(?:^|\s)-X(?:=|\s+)main\.(?:version|paneRuntimeSourceLinux)=[^\s"']+`)
	fleetRuntimeDebugFlag := regexp.MustCompile(`(?:^|\s)-(?:s|w)(?:=(?:true|false))?(\s|$)`)
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != "linux" || settings["GOARCH"] != "amd64" || settings["CGO_ENABLED"] != "0" {
		return ""
	}
	source := ""
	if match := fleetRuntimeSourceFlag.FindStringSubmatch(settings["-ldflags"]); len(match) == 2 {
		source = match[1]
	}
	if source == "" && len(legacy) > 0 {
		f, err := os.Open(path)
		if err != nil {
			return ""
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return ""
		}
		source = legacy[hex.EncodeToString(h.Sum(nil))]
	}
	if !validRuntimeDigest(source) {
		return ""
	}
	source = fleetCanonicalPaneRuntimeSource(source)
	// Source equality alone cannot waive a new Go runtime, race/CPU/compiler
	// settings, or semantic linker overrides. Ignore only provenance/version
	// metadata, which is expected to differ for a daemon-only release.
	var rows []string
	rows = append(rows, source, info.GoVersion)
	for key, value := range settings {
		if key == "vcs" || strings.HasPrefix(key, "vcs.") {
			continue
		}
		if key == "-ldflags" {
			value = fleetRuntimeMetadataFlag.ReplaceAllString(value, " ")
			// Symbol/DWARF stripping changes the artifact, not executable pane
			// behavior. Iterate because adjacent flags share a whitespace match.
			for !strings.ContainsAny(value, `"'`) && fleetRuntimeDebugFlag.MatchString(value) {
				value = fleetRuntimeDebugFlag.ReplaceAllString(value, " ")
			}
			value = strings.Join(strings.Fields(value), " ")
			if value == "" {
				continue
			}
		}
		rows = append(rows, key+"="+value)
	}
	sort.Strings(rows)
	digest := sha256.Sum256([]byte(strings.Join(rows, "\n")))
	return fmt.Sprintf("%x", digest)
}

// One audited metadata-only transition, not a version-based compatibility
// waiver. Between source 1d0d1e9af and release 3f17c7246 (0.0.1082), the pane
// closure changed only because shared/providers added HarnessFacts.Command,
// its three constant values, and validation of those same constant names.
// Pane code never reads Command; Harness is excluded from provider JSON.
// Moving shortcut installation metadata back to the daemon restores the first
// closure exactly. Accept wrappers started during the intervening release too.
// Compiler/runtime/CPU/semantic linker settings are STILL hashed above. Any
// other source digest (including future provider changes) remains distinct.
func fleetCanonicalPaneRuntimeSource(source string) string {
	if source == "63569694593fdb92a5d6ba15f7f3c235601d31aeb394f81b1e2a44bc98690eae" {
		return "a6a8750476f5da672873e521a4e515ed9de14c671e92a9546d558aa96a13c21f"
	}
	return source
}
