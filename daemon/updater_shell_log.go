package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const shellLaunchLogMaxBytes int64 = 512 * 1024

// shellLaunchLog is intentionally separate from daemon.log: daemon.log is
// truncated at boot, while installer-launch diagnostics must survive a restart
// (which is also what a successful installer normally causes).
type shellLaunchLog struct {
	path string
	mu   sync.Mutex
}

func newShellLaunchLog(dlDir string) *shellLaunchLog {
	return &shellLaunchLog{path: filepath.Join(dlDir, "launch.log")}
}

// event appends one JSON object per line. It is best-effort by design: losing a
// diagnostic must never turn a verified installer download into a failed one.
func (l *shellLaunchLog) event(ev string, fields map[string]string) {
	if l == nil {
		return
	}
	entry := make(map[string]string, len(fields)+2)
	entry["ts"] = time.Now().UTC().Format(time.RFC3339)
	entry["event"] = ev
	for k, v := range fields {
		if v != "" {
			entry[k] = v
		}
	}
	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("update: shell launch log encode failed: %v", err)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		log.Printf("update: shell launch log mkdir failed: %v", err)
		return
	}
	if info, statErr := os.Stat(l.path); statErr == nil && info.Size() > shellLaunchLogMaxBytes {
		rotated := l.path + ".1"
		_ = os.Remove(rotated)
		if renameErr := os.Rename(l.path, rotated); renameErr != nil {
			log.Printf("update: shell launch log rotate failed: %v", renameErr)
		}
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("update: shell launch log open failed: %v", err)
		return
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		log.Printf("update: shell launch log append failed: %v", err)
	}
	if err = f.Close(); err != nil {
		log.Printf("update: shell launch log close failed: %v", err)
	}
	log.Printf("update: shell launch event=%s fields=%v", ev, fields)
}
