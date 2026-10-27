package main

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const ownerRetirementLeaseDuration = 2 * time.Second
const (
	FrameOwnerRetirePrepare FrameType = 0x09
	FrameOwnerRetireReply   FrameType = 0x0a
	FrameOwnerRetireCommit  FrameType = 0x0b
	FrameOwnerRetireAbort   FrameType = 0x0c
)

type ownerRetirementFrozen interface {
	release()
	commit() error
}
type ownerRetirementReply struct {
	Token     string                `json:"token"`
	Ready     bool                  `json:"ready,omitempty"`
	Committed bool                  `json:"committed,omitempty"`
	Error     string                `json:"error,omitempty"`
	Proof     *ownerRetirementProof `json:"proof,omitempty"`
}
type ownerAgentRetirementLease struct {
	conn     net.Conn
	token    string
	frozen   ownerRetirementFrozen
	deadline time.Time
	timer    *time.Timer
}
type ownerAgentRetirement struct {
	mu              sync.Mutex
	outputMu        sync.Mutex
	paneID          string
	inputMu         *sync.Mutex
	connections     func() int
	connectionLive  func(net.Conn) bool
	inputFence      ownerRetirementInputFence
	submissionFence ownerRetirementInputFence
	pendingInputs   int
	inputSequence   uint64
	lease           *ownerAgentRetirementLease
}

// Called in the SAME ordered frame reader as terminal input. This is the
// barrier which a second socket or daemon-only process sample cannot provide.
func (r *ownerAgentRetirement) prepare(conn net.Conn, req ownerRetirementRequest, sequence uint64) ownerRetirementReply {
	reply := ownerRetirementReply{Token: req.Token}
	r.mu.Lock()
	reject := func(reason string) ownerRetirementReply { r.mu.Unlock(); reply.Error = reason; return reply }
	if r.inputSequence != sequence {
		return reject("terminal activity changed after retirement request")
	}
	if req.Token == "" || len(req.Token) > 128 || r.paneID == "" || req.PaneID != r.paneID {
		return reject("retirement pane identity mismatch")
	}
	if r.lease != nil {
		return reject("a retirement lease is already active")
	}
	if r.connections == nil || r.connections() != 1 || r.connectionLive == nil || !r.connectionLive(conn) {
		return reject("another daemon connection makes input ownership uncertain")
	}
	if r.pendingInputs != 0 {
		return reject("terminal input is waiting for delivery")
	}
	if !r.inputMu.TryLock() {
		return reject("terminal input or geometry is in progress")
	}
	deadline := time.Now().Add(ownerRetirementLeaseDuration)
	lease := &ownerAgentRetirementLease{conn: conn, token: req.Token, deadline: deadline}
	r.lease = lease
	inputFence := r.inputFence
	lease.timer = time.AfterFunc(time.Until(deadline), func() { r.abort(conn, req.Token) })
	r.mu.Unlock()
	// No coordinator lock spans provider/procfs I/O. The independent timer can
	// release the input fence even if a filesystem read stalls.
	frozen, proof, err := prepareOwnerIdleProof(req, inputFence, deadline)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease != lease || !time.Now().Before(deadline) {
		if frozen != nil {
			frozen.release()
		}
		if r.lease == lease {
			r.releaseLocked()
		}
		reply.Error = "retirement lease cancelled or expired during proof"
		return reply
	}
	if err != nil {
		r.releaseLocked()
		reply.Error = err.Error()
		return reply
	}
	lease.frozen = frozen
	reply.Ready = true
	reply.Proof = &proof
	return reply
}

func (r *ownerAgentRetirement) releaseLocked() {
	if r.lease == nil {
		return
	}
	lease := r.lease
	r.lease = nil
	if lease.timer != nil {
		lease.timer.Stop()
	}
	if lease.frozen != nil {
		lease.frozen.release()
	}
	r.inputMu.Unlock()
}
func (r *ownerAgentRetirement) abort(conn net.Conn, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease != nil && r.lease.conn == conn && (token == "" || r.lease.token == token) {
		r.releaseLocked()
	}
}
func (r *ownerAgentRetirement) input(payload []byte) {
	r.mu.Lock()
	// Input cancels, rather than being discarded to make cleanup succeed.
	r.releaseLocked()
	r.pendingInputs++
	r.inputSequence++
	sequence := r.inputSequence
	single := r.connections != nil && r.connections() == 1
	priorSubmission := r.submissionFence
	protocolOnly := ownerRetirementProtocolReplyOnly(payload)
	r.mu.Unlock()
	// Terminal query/focus replies still cancel a live close, but do not invent
	// an unsent user request. Mouse/key/paste traffic remains conservative.
	if protocolOnly {
		return
	}
	fence := ownerRetirementInputFence{Seen: true}
	submitted := bytes.ContainsAny(payload, "\r\n")
	if submitted {
		fence.UnresolvedSubmission = true
		// Once an earlier submission is unknown, a subsequent completion is
		// not evidence that this later queued request was consumed.
		if single && !priorSubmission.UnresolvedSubmission && bytes.Count(payload, []byte("\r"))+bytes.Count(payload, []byte("\n")) == 1 {
			fence = captureOwnerInputFence(r.paneID, priorSubmission)
		}
	}
	r.mu.Lock()
	if sequence == r.inputSequence {
		r.inputFence = fence
		if submitted {
			r.submissionFence = fence
		}
	} else {
		r.inputFence = ownerRetirementInputFence{Seen: true, UnresolvedSubmission: submitted}
		if submitted {
			r.submissionFence = r.inputFence
		}
	}
	r.mu.Unlock()
}
func (r *ownerAgentRetirement) inputDone() {
	r.mu.Lock()
	r.pendingInputs--
	r.mu.Unlock()
}

func (r *ownerAgentRetirement) sequence() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inputSequence
}
func (r *ownerAgentRetirement) attached() {
	r.mu.Lock()
	r.inputSequence++
	defer r.mu.Unlock()
	r.releaseLocked()
}
func (r *ownerAgentRetirement) commit(conn net.Conn, token string) ownerRetirementReply {
	reply := ownerRetirementReply{Token: token}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lease == nil || r.lease.conn != conn || r.lease.token != token || r.lease.frozen == nil {
		reply.Error = "retirement lease is absent or changed"
		return reply
	}
	if !time.Now().Before(r.lease.deadline) {
		r.releaseLocked()
		reply.Error = "retirement lease expired"
		return reply
	}
	if r.connections() == 1 && r.connectionLive(conn) {
		if err := r.lease.frozen.commit(); err == nil {
			reply.Committed = true
		} else {
			reply.Error = err.Error()
		}
	} else {
		reply.Error = fmt.Sprint("daemon connections changed before retirement")
	}
	r.releaseLocked()
	return reply
}

// exec.Cmd resolves duplicate environment entries last-wins. Bind capability
// to the effective child value rather than an inherited daemon pane ID.
func ownerRetirementPaneFromEnv(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], "SOLU_PANE_ID="); ok {
			return value
		}
	}
	return ""
}

func ownerRetirementProtocolReplyOnly(payload []byte) bool {
	if !isTerminalReplyOnly(payload) {
		return false
	}
	for len(payload) > 0 {
		n := consumeReplySeq(payload)
		if n <= 0 {
			return false
		}
		sequence := payload[:n]
		if len(sequence) < 3 {
			return false
		}
		if sequence[1] == '[' {
			switch sequence[len(sequence)-1] {
			case 'M', 'm', '~':
				return false
			}
		} else if sequence[1] == ']' {
			// Color query replies cannot submit or populate a prompt; clipboard and
			// arbitrary OSC/DCS strings are deliberately not assumed harmless.
			if !(bytes.HasPrefix(sequence, []byte("\x1b]10;rgb:")) || bytes.HasPrefix(sequence, []byte("\x1b]11;rgb:")) || bytes.HasPrefix(sequence, []byte("\x1b]12;rgb:"))) {
				return false
			}
		} else {
			return false
		}
		payload = payload[n:]
	}
	return true
}
