package main

import "strings"

// A test may supply exactly the same observation contract as the Linux reader.
// Production terminal panes always use the reader; there is no PTY fallback.
type turnProviderPane interface {
	providerTurnObservation(*turnProviderReader, int64) turnProviderObservation
}

func (a turnPaneAdapter) providerTurnObservation(r *turnProviderReader, nowMs int64) turnProviderObservation {
	var source *turnProviderSource
	if r.reopen != nil {
		source = r.reopen()
	}
	if source == nil {
		source = openTurnProviderSource(a.p)
	}
	return r.read(source, nowMs)
}
func providerTurnKey(at providerTurn) string { return at.Agent + ":" + at.Session + ":" + at.ID }

func (t *turnTracker) observeProvider(p turnPane, st *paneTurnState, nowMs int64) {
	source, ok := p.(turnProviderPane)
	if !ok {
		return
	}
	observation := source.providerTurnObservation(&st.providerReader, nowMs)
	t.mu.Lock()
	st.title = p.title()
	st.providerUncertain = observation.uncertain || !observation.available && (st.active != nil || p.running() && p.agent() != "")
	if !observation.available {
		// Losing evidence cannot manufacture a completion. A previously observed
		// active turn stays unresolved until its matching provider boundary appears.
		t.mu.Unlock()
		return
	}
	priorCheckpoint := st.providerCheckpoint
	st.providerCheckpoint = observation.checkpoint
	st.providerSource = observation.source
	t.mu.Unlock()
	for _, ended := range observation.completed {
		key := providerTurnKey(ended)
		t.mu.Lock()
		duplicate := st.providerCompleted == key
		for _, rec := range st.history {
			if rec.Source == ended.Agent+":"+ended.Session && rec.SourceTurnID == ended.ID {
				duplicate = true
				break
			}
		}
		if duplicate {
			if st.active != nil && st.active.provider != nil && providerTurnKey(*st.active.provider) == key {
				st.active = nil
			}
			t.mu.Unlock()
			continue
		}
		st.providerCompleted = key
		at := st.active
		if at != nil && at.provider != nil && providerTurnKey(*at.provider) == key {
			at.provider = &ended
			st.active = nil
			st.lastEndMs = ended.EndedMs
			t.mu.Unlock()
			// Completion time is independent of the snapshot queue. Enrich
			// this same record later, without renumbering it or resetting age.
			t.record(p.id(), turnRecord{Agent: ended.Agent, Source: ended.Agent + ":" + ended.Session, SourceTurnID: ended.ID,
				StartedMs: ended.StartedMs, EndedMs: ended.EndedMs, Outcome: ended.Outcome, Note: "Diff capture pending"})
			t.finish(p.id(), at, ended.EndedMs)
		} else {
			if at != nil && at.provider != nil && at.startedMs <= ended.StartedMs {
				st.active = nil
			}
			// First discovery/reconnect can recover an authentic completion, but not
			// the repository's historical start tree. Publish no invented diff/unread.
			t.mu.Unlock()
			t.record(p.id(), turnRecord{Agent: ended.Agent, Source: ended.Agent + ":" + ended.Session, SourceTurnID: ended.ID,
				StartedMs: ended.StartedMs, EndedMs: ended.EndedMs, Outcome: ended.Outcome, Historical: observation.initial && ended.EndedMs <= t.lifecycleCutoffMs,
				Note: "Provider completion recovered; no snapshots at its boundaries"})
		}
	}
	t.mu.Lock()
	if observation.active == nil {
		// A source/conversation change may invalidate a previous active turn. Its
		// disappearance is not evidence that it succeeded.
		if st.active != nil && st.active.provider != nil && (observation.initial || !strings.HasPrefix(providerTurnKey(*st.active.provider), st.providerSource+":")) {
			st.providerUncertain = true
			st.active = nil
		}
		t.mu.Unlock()
		return
	}
	incoming := *observation.active
	if st.active != nil && st.active.provider != nil && providerTurnKey(*st.active.provider) == providerTurnKey(incoming) {
		if observation.checkpoint != nil && (priorCheckpoint == nil || priorCheckpoint.Offset != observation.checkpoint.Offset) {
			t.persistLocked(p.id(), st)
		}
		t.mu.Unlock()
		return
	}
	at := &activeTurn{paneID: p.id(), agent: incoming.Agent, startedMs: incoming.StartedMs, provider: &incoming, baseDone: make(chan struct{})}
	if observation.initial {
		at.note = "Turn already active when observed; diff covers the observed interval only"
	}
	st.active = at
	t.persistLocked(p.id(), st)
	t.mu.Unlock()
	t.startBase(at, p.cwd())
}
