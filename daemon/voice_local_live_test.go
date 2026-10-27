//go:build livevoice

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"soluterminal-shared/voiceclean"
)

// TestLiveLocalCleanup measures local cleanup on a real Claude Code login.
// It is never part of the normal suite (build tag livevoice). Example:
//
//	LIVE_JOBS=jobs.jsonl LIVE_OUT=out.jsonl LIVE_CLAUDE_BIN=$(command -v claude) \
//	  go test -tags livevoice -run TestLiveLocalCleanup -timeout 90m .
//
// Each job line is {clip, cond, id, ctx, raw, context}. LIVE_WARM_MS (default
// 2500) is how long the spare gets to start, standing in for the recording.
func TestLiveLocalCleanup(t *testing.T) {
	jobsPath, outPath, bin := os.Getenv("LIVE_JOBS"), os.Getenv("LIVE_OUT"), os.Getenv("LIVE_CLAUDE_BIN")
	if jobsPath == "" || outPath == "" || bin == "" {
		t.Skip("set LIVE_JOBS, LIVE_OUT and LIVE_CLAUDE_BIN")
	}
	warm := 2500 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv("LIVE_WARM_MS")); err == nil {
		warm = time.Duration(v) * time.Millisecond
	}
	c := newLocalCleaner()
	c.bin = func() (string, error) { return bin, nil }
	c.deadline = 30 * time.Second // measure the tail instead of cutting it off

	in, err := os.Open(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var job map[string]any
		if err := json.Unmarshal(sc.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		raw, _ := job["raw"].(string)
		termContext, _ := job["context"].(string)
		job["provider"] = "local"
		delete(job, "context")
		if len(raw) < voiceclean.MinCleanupBytes {
			job["status"], job["text"], job["ms"] = "short-skip", raw, 0
		} else {
			if warm > 0 {
				c.Prewarm(newCleanVariant(voiceclean.ProfileCode, termContext))
				time.Sleep(warm)
			}
			start := time.Now()
			res := c.Clean(context.Background(), voiceclean.ProfileCode, raw, voiceclean.TrimContext(termContext))
			job["ms"] = float64(time.Since(start).Microseconds()) / 1000
			job["status"], job["text"], job["detail"] = res.Status, res.Text, res.Detail
			job["input_tokens"], job["output_tokens"] = res.InputTokens, res.OutputTokens
		}
		if err := enc.Encode(job); err != nil {
			t.Fatal(err)
		}
		t.Logf("%v ctx=%v %v %vms | %v", job["clip"], job["ctx"], job["status"], job["ms"], job["text"])
	}
}
