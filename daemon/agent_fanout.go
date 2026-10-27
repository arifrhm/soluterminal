package main

import (
	"net"
	"time"
)

// agentFanout is the pane agent's set of daemon connections and how PTY
// output reaches each one. Every method expects the caller to hold the
// agent's subsMu: the PTY reader holds it while it appends to the ring and
// fans out, so a new connection's replay snapshot and its stream start at
// the same byte.
//
// Terminal (PTY) connections are fed by a ringPump that reads the output ring
// by position, so a burst never drops bytes on the way to the daemon. No-PTY
// connections (Claude JSONL, Codex JSON-RPC) keep a direct channel with
// blocking sends: losing one chunk there can lose an approval or a turn
// boundary, so they take local-socket backpressure instead.
type agentFanout struct {
	ring  *RingBuffer
	noPty bool
	subs  map[*net.UnixConn]chan []byte // the channel each connection's writer consumes
	pumps map[*net.UnixConn]*ringPump   // PTY connections only
}

func newAgentFanout(ring *RingBuffer, noPty bool) *agentFanout {
	return &agentFanout{
		ring:  ring,
		noPty: noPty,
		subs:  map[*net.UnixConn]chan []byte{},
		pumps: map[*net.UnixConn]*ringPump{},
	}
}

// addLocked registers conn and returns the channel its writer drains. A PTY
// connection streams from the ring's current end, which is where a replay
// snapshot taken under the same lock stops.
func (f *agentFanout) addLocked(conn *net.UnixConn) chan []byte {
	var ch chan []byte
	if f.noPty {
		ch = make(chan []byte, 64)
	} else {
		pump := newRingPump(f.ring, f.ring.TotalWritten())
		f.pumps[conn] = pump
		ch = pump.out
		go pump.run()
	}
	f.subs[conn] = ch
	return ch
}

// removeLocked unregisters conn. A PTY connection's pump closes the writer's
// channel itself once it has handed over what was already in the ring.
func (f *agentFanout) removeLocked(conn *net.UnixConn) {
	ch, ok := f.subs[conn]
	if !ok {
		return
	}
	if pump := f.pumps[conn]; pump != nil {
		pump.close()
		delete(f.pumps, conn)
	} else {
		close(ch)
	}
	delete(f.subs, conn)
}

func (f *agentFanout) countLocked() int { return len(f.subs) }

func (f *agentFanout) hasLocked(conn *net.UnixConn) bool {
	_, ok := f.subs[conn]
	return ok
}

// fanoutLocked delivers a chunk the caller has just written to the ring.
// PTY connections are only nudged: their pump copies from the ring at its own
// pace, so the reader never waits on a slow socket and nothing is dropped.
func (f *agentFanout) fanoutLocked(b []byte) {
	for conn, ch := range f.subs {
		if pump := f.pumps[conn]; pump != nil {
			pump.signal()
			continue
		}
		ch <- b
	}
}

// detachAllLocked removes every connection for the exit broadcast. It returns
// the connections and a wait function that gives each pump up to `limit` to
// hand its last bytes to the writer; call it after releasing the lock.
func (f *agentFanout) detachAllLocked(limit time.Duration) ([]*net.UnixConn, func()) {
	conns := make([]*net.UnixConn, 0, len(f.subs))
	pumps := make([]*ringPump, 0, len(f.pumps))
	for conn, ch := range f.subs {
		conns = append(conns, conn)
		if pump := f.pumps[conn]; pump != nil {
			pump.close()
			pumps = append(pumps, pump)
		} else {
			close(ch)
		}
	}
	f.subs = map[*net.UnixConn]chan []byte{}
	f.pumps = map[*net.UnixConn]*ringPump{}
	wait := func() {
		deadline := time.After(limit)
		for _, pump := range pumps {
			select {
			case <-pump.done:
			case <-deadline:
				return
			}
		}
	}
	return conns, wait
}
