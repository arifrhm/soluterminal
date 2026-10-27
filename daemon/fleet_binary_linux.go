//go:build linux

package main

import (
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type fleetLinuxCacheKey struct {
	PID       int
	StartTime uint64
	Device    uint64
	Inode     uint64
}

type fleetLinuxCacheValue struct {
	Version string
	Runtime string
	Err     string
}

type fleetLinuxTargetCacheKey struct {
	Path          string
	Identity      fleetFileIdentity
	MarkerSize    int64
	MarkerModNano int64
}

type fleetLinuxTargetCacheValue struct {
	Target fleetTargetBinary
	Err    string
}

type linuxFleetBinaryPlatform struct {
	mu          sync.Mutex
	cache       map[fleetLinuxCacheKey]fleetLinuxCacheValue
	targetCache map[fleetLinuxTargetCacheKey]fleetLinuxTargetCacheValue
	legacy      map[string]string
	runtimes    map[fleetFileIdentity]string
}

func newFleetBinaryPlatform() fleetBinaryPlatform {
	return &linuxFleetBinaryPlatform{
		cache:       make(map[fleetLinuxCacheKey]fleetLinuxCacheValue),
		targetCache: make(map[fleetLinuxTargetCacheKey]fleetLinuxTargetCacheValue),
		runtimes:    make(map[fleetFileIdentity]string),
	}
}

func (*linuxFleetBinaryPlatform) Supported() bool { return true }

func (p *linuxFleetBinaryPlatform) Target(path string) (fleetTargetBinary, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return fleetTargetBinary{}, errors.New("installed executable path is empty")
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	identity, err := fleetStatIdentity(path)
	if err != nil {
		return fleetTargetBinary{}, fmt.Errorf("stat installed executable %s: %w", path, err)
	}
	cacheKey := fleetLinuxTargetCacheKey{Path: path, Identity: identity}
	if marker, markerErr := os.Stat(path + ".version"); markerErr == nil {
		cacheKey.MarkerSize = marker.Size()
		cacheKey.MarkerModNano = marker.ModTime().UnixNano()
	}
	p.mu.Lock()
	cached, ok := p.targetCache[cacheKey]
	p.mu.Unlock()
	if ok {
		if cached.Err != "" {
			return fleetTargetBinary{}, errors.New(cached.Err)
		}
		return cached.Target, nil
	}
	version, versionErr := fleetReadBinaryVersion(path, true)
	target := fleetTargetBinary{Path: path, Version: version, Identity: identity}
	p.mu.Lock()
	// Optional migration receipts bind legacy builds to their exact SHA-256.
	// They never grant compatibility to a version label or an arbitrary inode.
	p.legacy = fleetReadLegacyRuntimeReceipts(path + ".pane-runtime-legacy.json")
	p.runtimes = make(map[fleetFileIdentity]string)
	target.Runtime = fleetReadPaneRuntime(path, nil)
	p.runtimes[identity] = target.Runtime
	p.mu.Unlock()
	cacheValue := fleetLinuxTargetCacheValue{Target: target}
	if versionErr != nil {
		cacheValue.Err = fmt.Sprintf("inspect installed executable %s: %v", path, versionErr)
	}
	p.mu.Lock()
	// At most one installed target identity/marker tuple is relevant.
	p.targetCache = map[fleetLinuxTargetCacheKey]fleetLinuxTargetCacheValue{cacheKey: cacheValue}
	p.mu.Unlock()
	if cacheValue.Err != "" {
		return fleetTargetBinary{}, errors.New(cacheValue.Err)
	}
	return target, nil
}

func (p *linuxFleetBinaryPlatform) Process(pid int, reportedVersion string) (fleetProcessBinary, error) {
	if pid <= 0 {
		return fleetProcessBinary{}, fmt.Errorf("invalid pid %d", pid)
	}
	procExe := fmt.Sprintf("/proc/%d/exe", pid)
	executable, err := os.Readlink(procExe)
	if err != nil {
		return fleetProcessBinary{}, fmt.Errorf("read %s: %w", procExe, err)
	}
	identity, err := fleetStatIdentity(procExe)
	if err != nil {
		return fleetProcessBinary{}, fmt.Errorf("stat %s: %w", procExe, err)
	}
	startTime, err := fleetProcessStartTime(pid)
	if err != nil {
		return fleetProcessBinary{}, err
	}
	key := fleetLinuxCacheKey{PID: pid, StartTime: startTime, Device: identity.Device, Inode: identity.Inode}
	p.mu.Lock()
	cached, ok := p.cache[key]
	p.mu.Unlock()
	if !ok {
		version, versionErr := fleetReadBinaryVersion(procExe, false)
		cached.Version = version
		p.mu.Lock()
		fingerprint, found := p.runtimes[identity]
		if !found {
			fingerprint = fleetReadPaneRuntime(procExe, p.legacy)
			p.runtimes[identity] = fingerprint
		}
		cached.Runtime = fingerprint
		p.mu.Unlock()
		if versionErr != nil {
			cached.Err = versionErr.Error()
		}
		p.mu.Lock()
		// PID reuse and an exec-in-place both invalidate the prior parse. Keep
		// only the current identity for this PID so the cache is bounded by the
		// Manager-owned fleet, not by daemon lifetime.
		for old := range p.cache {
			if old.PID == pid && old != key {
				delete(p.cache, old)
			}
		}
		p.cache[key] = cached
		p.mu.Unlock()
	}
	version := cached.Version
	if version == "" {
		version = strings.TrimSpace(reportedVersion)
	}
	return fleetProcessBinary{
		PID: pid, StartTime: startTime, Identity: identity, Executable: executable,
		Version: version, Deleted: strings.HasSuffix(executable, " (deleted)"), Runtime: cached.Runtime,
	}, nil
}

func fleetStatIdentity(path string) (fleetFileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fleetFileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fleetFileIdentity{}, errors.New("stat identity unavailable")
	}
	return fleetFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func fleetProcessStartTime(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("read process start time for pid %d: %w", pid, err)
	}
	// comm is parenthesized and may contain spaces or ')'. The final ')' is
	// followed by field 3 (state); starttime is field 22, hence index 19.
	closeParen := strings.LastIndexByte(string(b), ')')
	if closeParen < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat: missing command terminator", pid)
	}
	fields := strings.Fields(string(b[closeParen+1:]))
	if len(fields) <= 19 {
		return 0, fmt.Errorf("parse /proc/%d/stat: too few fields", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return start, nil
}

// fleetReadBinaryVersion follows the release-ldflags convention in decreasing
// order of authority: the actual main.version linker data, Go build metadata,
// then the updater-maintained sidecar. If independent embedded and sidecar
// values disagree the inventory fails closed instead of picking one silently.
func fleetReadBinaryVersion(path string, allowMarker bool) (string, error) {
	embedded, embeddedErr := fleetELFMainVersion(path)
	if embedded == "" {
		if buildVersion, err := fleetGoBuildInfoVersion(path); err == nil {
			embedded = buildVersion
		} else if embeddedErr == nil {
			embeddedErr = err
		}
	}
	marker := ""
	if allowMarker {
		marker = readVersionMarker(path)
	}
	if embedded != "" && marker != "" && embedded != marker {
		return "", fmt.Errorf("embedded version %s disagrees with version marker %s", embedded, marker)
	}
	if embedded != "" {
		return embedded, nil
	}
	if marker != "" {
		return marker, nil
	}
	// An unknown version does not make inode inventory unsafe. Identity still
	// conclusively classifies a replaced process and the API reports version="".
	_ = embeddedErr
	return "", nil
}

var fleetMainVersionLDFlag = regexp.MustCompile(`(?:^|\s)-X(?:=|\s+)main\.version=([^\s"']+)`)

func fleetGoBuildInfoVersion(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, setting := range info.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		match := fleetMainVersionLDFlag.FindStringSubmatch(setting.Value)
		if len(match) == 2 {
			return strings.TrimSpace(match[1]), nil
		}
	}
	return "", errors.New("main.version ldflag not present in Go build info")
}

func fleetELFMainVersion(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	symbols, _ := f.Symbols()
	if dynamic, dynamicErr := f.DynamicSymbols(); dynamicErr == nil {
		symbols = append(symbols, dynamic...)
	}
	for _, symbol := range symbols {
		if symbol.Name != "main.version.str" {
			continue
		}
		length := symbol.Size
		if length == 0 || length > 1024 {
			length = 256
		}
		b, readErr := fleetELFReadVirtual(f, symbol.Value, length)
		if readErr != nil {
			return "", readErr
		}
		if nul := strings.IndexByte(string(b), 0); nul >= 0 {
			b = b[:nul]
		}
		if value := strings.TrimSpace(string(b)); value != "" {
			return value, nil
		}
	}
	for _, symbol := range symbols {
		if symbol.Name != "main.version" {
			continue
		}
		wordSize := uint64(8)
		if f.Class == elf.ELFCLASS32 {
			wordSize = 4
		}
		header, readErr := fleetELFReadVirtual(f, symbol.Value, wordSize*2)
		if readErr != nil {
			return "", readErr
		}
		var ptr, length uint64
		if wordSize == 8 {
			ptr = f.ByteOrder.Uint64(header[:8])
			length = f.ByteOrder.Uint64(header[8:16])
		} else {
			ptr = uint64(f.ByteOrder.Uint32(header[:4]))
			length = uint64(f.ByteOrder.Uint32(header[4:8]))
		}
		if length == 0 || length > 1024 {
			return "", fmt.Errorf("main.version string length %d is invalid", length)
		}
		b, readErr := fleetELFReadVirtual(f, ptr, length)
		if readErr != nil {
			return "", readErr
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", errors.New("main.version ELF symbol unavailable")
}

func fleetELFReadVirtual(f *elf.File, address, length uint64) ([]byte, error) {
	for _, section := range f.Sections {
		if section.Addr == 0 || address < section.Addr || address+length > section.Addr+section.Size {
			continue
		}
		reader := section.Open()
		if _, err := reader.Seek(int64(address-section.Addr), io.SeekStart); err != nil {
			return nil, err
		}
		b := make([]byte, length)
		if _, err := io.ReadFull(reader, b); err != nil {
			return nil, err
		}
		return b, nil
	}
	return nil, fmt.Errorf("ELF virtual address %#x is not file-backed", address)
}
