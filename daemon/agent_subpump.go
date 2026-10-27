package main

import (
	"log"
	"sync"
)

// ringPumpChunk caps one FrameData payload a pump hands to its writer. It
// stays well under MaxFramePayload and close to the PTY read size, so frames
// look like they always did.
const ringPumpChunk = 32 * 1024

// ringPump streams a terminal pane's output to one daemon connection by
// position in the agent's ring buffer.
//
// The agent used to push each PTY chunk into a 64-slot channel per
// connection and drop the chunk when the channel was full. During a burst
// (a fast `seq`, a big build log) the daemon briefly falls behind, so whole
// chunks vanished from the daemon's ring: the pane's stored history then had
// holes in the middle that every later replay, reload and agent read
// (read_pane_tail, ask_pane) inherited.
//
// A pump never drops: the PTY reader only nudges it, and the pump copies
// whatever is new from the ring at its own pace. The PTY reader never waits
// on a slow connection. Bytes are lost only if a connection falls a full ring
// (8 MiB) behind, and that is logged.
type ringPump struct {
	ring *RingBuffer
	out  chan []byte // consumed by handleAgentClient's writer; closed by run
	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	off  int64

	stopOnce sync.Once
}

func newRingPump(ring *RingBuffer, from int64) *ringPump {
	return &ringPump{
		ring: ring,
		out:  make(chan []byte, 4),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
		off:  from,
	}
}

// signal tells the pump new bytes are in the ring. It never blocks, so the
// PTY reader can call it while holding subsMu.
func (p *ringPump) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// close asks the pump to forward what is already in the ring and then close
// its output channel. Idempotent and non-blocking.
func (p *ringPump) close() {
	p.stopOnce.Do(func() { close(p.stop) })
}

func (p *ringPump) run() {
	defer close(p.done)
	defer close(p.out)
	for {
		p.forward()
		select {
		case <-p.wake:
		case <-p.stop:
			// Hand over whatever arrived before the stop, then end.
			p.forward()
			return
		}
	}
}

// forward hands every byte past p.off to the writer. The send blocks while
// the writer is busy; that is safe because the writer keeps draining the
// channel until it is closed, even after its socket fails.
func (p *ringPump) forward() {
	for {
		data, start := p.ring.ReadFrom(p.off, ringPumpChunk)
		if start > p.off {
			log.Printf("agent: connection fell %d bytes behind the output ring; those bytes were not forwarded", start-p.off)
		}
		if len(data) == 0 {
			p.off = start
			return
		}
		p.out <- data
		p.off = start + int64(len(data))
	}
}
