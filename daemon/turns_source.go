package main

import (
	"bytes"
	"io"
	"os"
)

const turnSourceReadLimit = 2 << 20

// Source opening is provider/platform specific. The descriptor is checked both
// before and after reading; contents are never exposed in the pane API.
type turnProviderSource struct {
	pid                 int // generation-validated provider process; not standalone close authority
	reopen              func() *turnProviderSource
	file                *os.File
	key, agent, session string
	valid               func() bool
}

type turnProviderReader struct {
	restore     *turnReaderCheckpoint
	reopen      func() *turnProviderSource
	key         string
	info        os.FileInfo
	offset      int64
	skipPartial bool
	parser      turnProviderParser
}

type turnProviderObservation struct {
	startedOffsets map[string]int64 // fresh-read boundary offsets; never persisted
	uncertain      bool
	checkpoint     *turnReaderCheckpoint
	source         string
	available      bool
	initial        bool
	active         *providerTurn
	completed      []providerTurn
}

func (r *turnProviderReader) read(source *turnProviderSource, nowMs int64) turnProviderObservation {
	if source == nil {
		return turnProviderObservation{}
	}
	defer source.file.Close()
	info, err := source.file.Stat()
	if err != nil || !info.Mode().IsRegular() || !source.valid() {
		return turnProviderObservation{}
	}
	initial := r.key != source.key || r.info == nil || !os.SameFile(r.info, info) || info.Size() < r.offset
	candidate := *r
	if initial {
		candidate = turnProviderReader{key: source.key, info: info, parser: turnProviderParser{agent: source.agent, session: source.session}}
		if restored := r.restore; restored.validFor(source, info) {
			candidate.offset = restored.Offset
			candidate.skipPartial = restored.SkipPartial
			candidate.parser = turnProviderParser{agent: restored.Agent, session: restored.Session, active: restored.Active, claudeHead: restored.ClaudeHead, claudeFinal: restored.ClaudeFinal, museRoots: restored.MuseRoots, uncertain: restored.Uncertain}
			initial = false
		} else {
			candidate.parser.uncertain = r.restore != nil
			if info.Size() > turnSourceReadLimit {
				candidate.offset = info.Size() - turnSourceReadLimit
				candidate.skipPartial = true
				candidate.parser.uncertain = true
			}
		}
	}
	// Clone state mutated by parsing so failed ownership revalidation cannot
	// publish even part of another source's lifecycle.
	if candidate.parser.active != nil {
		at := *candidate.parser.active
		candidate.parser.active = &at
	}
	if candidate.parser.museRoots != nil {
		roots := map[string]bool{}
		for k, v := range candidate.parser.museRoots {
			roots[k] = v
		}
		candidate.parser.museRoots = roots
	}
	candidate.parser.completed = nil
	remaining := info.Size() - candidate.offset
	if remaining > turnSourceReadLimit {
		remaining = turnSourceReadLimit
	}
	data := make([]byte, remaining)
	n, err := source.file.ReadAt(data, candidate.offset)
	if err != nil && err != io.EOF {
		return turnProviderObservation{}
	}
	data = data[:n]
	startedOffsets := map[string]int64{}
	consumed := 0
	for {
		end := bytes.IndexByte(data[consumed:], '\n')
		if end < 0 {
			break
		}
		end += consumed
		if !candidate.skipPartial {
			priorKey := ""
			if candidate.parser.active != nil {
				priorKey = providerTurnKey(*candidate.parser.active)
			}
			candidate.parser.feed(data[consumed:end], nowMs)
			if candidate.parser.active != nil {
				key := providerTurnKey(*candidate.parser.active)
				if key != priorKey {
					startedOffsets[key] = candidate.offset + int64(end+1)
				}
			}
		}
		candidate.skipPartial = false
		consumed = end + 1
	}
	candidate.offset += int64(consumed)
	if consumed == 0 && len(data) == turnSourceReadLimit {
		// One oversized record must not stall every future boundary forever.
		candidate.offset += int64(len(data))
		candidate.skipPartial = true
		candidate.parser.uncertain = true
	}
	if !source.valid() {
		return turnProviderObservation{}
	}
	candidate.reopen = source.reopen
	*r = candidate
	out := turnProviderObservation{startedOffsets: startedOffsets, uncertain: candidate.parser.uncertain || candidate.offset < info.Size() && len(data) == turnSourceReadLimit, checkpoint: candidate.checkpoint(), source: source.agent + ":" + source.session, available: true, initial: initial, completed: append([]providerTurn(nil), candidate.parser.completed...)}
	if candidate.parser.active != nil {
		at := *candidate.parser.active
		out.active = &at
	}
	r.parser.completed = nil
	return out
}

// Only lifecycle identifiers and bounded cursor state cross daemon handoff.
// No transcript content, prompt, secret, pathname or open descriptor is saved.
type turnReaderCheckpoint struct {
	Key         string          `json:"key"`
	FileID      string          `json:"fileId"`
	Offset      int64           `json:"offset"`
	SkipPartial bool            `json:"skipPartial,omitempty"`
	Agent       string          `json:"agent"`
	Session     string          `json:"session"`
	Active      *providerTurn   `json:"active,omitempty"`
	ClaudeHead  string          `json:"claudeHead,omitempty"`
	ClaudeFinal bool            `json:"claudeFinal,omitempty"`
	MuseRoots   map[string]bool `json:"museRoots,omitempty"`
	Uncertain   bool            `json:"uncertain,omitempty"`
}

func (c *turnReaderCheckpoint) validFor(source *turnProviderSource, info os.FileInfo) bool {
	if c == nil || c.Offset < 0 || c.Offset > info.Size() || len(c.Key) > 1024 || len(c.FileID) > 128 || c.Key != source.key || c.FileID != turnFileIdentity(info) || c.Agent != source.agent || c.Session != source.session || len(c.Session) == 0 || len(c.Session) > 256 || len(c.ClaudeHead) > 256 || len(c.MuseRoots) > 64 {
		return false
	}
	if active := c.Active; active != nil {
		if active.Agent != c.Agent || active.Session != c.Session || active.ID == "" || len(active.ID) > 256 || active.StartedMs <= 0 || active.EndedMs != 0 || active.Outcome != "" {
			return false
		}
	}
	for id, root := range c.MuseRoots {
		if id == "" || len(id) > 256 || !root {
			return false
		}
	}
	return true
}

func (r *turnProviderReader) checkpoint() *turnReaderCheckpoint {
	if r.info == nil {
		return nil
	}
	checkpoint := &turnReaderCheckpoint{Key: r.key, FileID: turnFileIdentity(r.info), Offset: r.offset, SkipPartial: r.skipPartial,
		Agent: r.parser.agent, Session: r.parser.session, ClaudeHead: r.parser.claudeHead, ClaudeFinal: r.parser.claudeFinal, Uncertain: r.parser.uncertain}
	if r.parser.active != nil {
		active := *r.parser.active
		checkpoint.Active = &active
	}
	if len(r.parser.museRoots) > 0 {
		checkpoint.MuseRoots = map[string]bool{}
		for id, v := range r.parser.museRoots {
			checkpoint.MuseRoots[id] = v
		}
	}
	return checkpoint
}
