package main

import (
	"context"
	"sync"
	"time"
)

// resizableSem is a counting semaphore whose capacity may change while
// holders are in flight. It replaces the fixed buffered channels the engine
// used for task and heavy-operation capacity so Settings can retune those caps
// without a daemon restart.
//
// Invariants:
//   - In-flight holders keep their slots. SetCap never revokes an admission;
//     it only governs which NEW acquisitions are admitted. Release is the only
//     path that decrements inUse.
//   - Growing wakes every queued waiter immediately, so raising a cap admits
//     already-waiting work without any holder releasing.
//   - Shrinking below the current holder count can never deadlock waiters:
//     both Release and SetCap broadcast, and every waiter re-evaluates
//     inUse < capacity after each wake. Admissions simply stay blocked until
//     enough releases bring inUse under the new cap; releases never block.
//   - notify is a broadcast channel — closed and replaced under the lock — so
//     a waiter that captured the previous channel always observes the wake and
//     re-checks against the current capacity.
//   - Capacity has a floor of 1: a zero cap would be an unusable pool, and the
//     resolvers already clamp their inputs.
//   - Exclusivity (AcquireAll) is a flag, not a slot count, so it cannot be
//     undercut by a concurrent SetCap that raises the cap.
//   - A class reserve affects only new admissions. It never revokes a holder;
//     Kick wakes waiters when an external class counter changes.
type resizableSem struct {
	mu              sync.Mutex
	capacity        int
	inUse           int
	waiters         int
	priorityWaiters int
	priorityBurst   int
	exclusive       bool
	// exclusivePending counts bounded janitor admission barriers. It blocks
	// only new holders after the ordinary non-hoarding wait has elapsed;
	// current holders are never interrupted and Release always drains them.
	// A count prevents one concurrent sweep from reopening another's barrier.
	exclusivePending int
	notify           chan struct{}
}

// Bound priority to three admissions before an ordinary queued waiter gets
// a turn. Only the repository lock holder uses this class on the task pool.
const taskPriorityBurst = 3

func newWfResizableSem(capacity int) *resizableSem {
	return &resizableSem{capacity: max(1, capacity), notify: make(chan struct{})}
}

// TryAcquire takes a slot when one is free under the live cap. It never blocks.
func (s *resizableSem) TryAcquire() bool {
	return s.TryAcquireReserve(0)
}

func (s *resizableSem) TryAcquireReserve(reserve int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admissibleReserveLocked(reserve) && s.classAdmissibleLocked(false) {
		s.inUse++
		s.priorityBurst = 0
		return true
	}
	return false
}

// Acquire blocks until a slot is free under the live cap or ctx is done. A
// done context always loses the race for a slot, so a cancelled task cannot
// take capacity on its way out.
func (s *resizableSem) Acquire(ctx context.Context) error {
	return s.AcquireReserve(ctx, nil)
}

func (s *resizableSem) AcquireReserve(ctx context.Context, reserve func() int) error {
	return s.acquireClass(ctx, reserve, false)
}

func (s *resizableSem) AcquirePriority(ctx context.Context) error {
	return s.acquireClass(ctx, nil, true)
}

func (s *resizableSem) classAdmissibleLocked(priority bool) bool {
	if priority {
		return s.priorityBurst < taskPriorityBurst || s.waiters == s.priorityWaiters
	}
	return s.priorityWaiters == 0 || s.priorityBurst >= taskPriorityBurst
}

func (s *resizableSem) acquireClass(ctx context.Context, reserve func() int, priority bool) error {
	waiting := false
	defer func() {
		if !waiting {
			return
		}
		s.mu.Lock()
		s.waiters--
		if priority {
			s.priorityWaiters--
		}
		// Cancellation or a fairness turn can unblock the other class even
		// without a holder releasing capacity.
		if priority || s.priorityWaiters > 0 {
			s.broadcastLocked()
		}
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		if !waiting {
			s.waiters++
			if priority {
				s.priorityWaiters++
			}
			waiting = true
		}
		r := 0
		if reserve != nil {
			r = reserve()
		}
		if s.admissibleReserveLocked(r) && s.classAdmissibleLocked(priority) {
			s.inUse++
			if priority {
				s.priorityBurst = min(taskPriorityBurst, s.priorityBurst+1)
			} else {
				s.priorityBurst = 0
			}
			s.mu.Unlock()
			return nil
		}
		wake := s.notify
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Release returns one slot. It is safe to call only once per successful
// acquisition and never blocks.
func (s *resizableSem) Release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	s.broadcastLocked()
	s.mu.Unlock()
}

// SetCap retunes the cap for future admissions. Existing holders are untouched.
func (s *resizableSem) SetCap(n int) {
	if s == nil {
		return
	}
	next := max(1, n)
	s.mu.Lock()
	if next != s.capacity {
		s.capacity = next
		s.broadcastLocked()
	}
	s.mu.Unlock()
}

func (s *resizableSem) Kick() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.broadcastLocked()
	s.mu.Unlock()
}

// Cap and InUse report 0 for a nil semaphore. Read models are built from
// partially-constructed engines in several places (and previously read
// cap(nil chan), which is 0), so an unwired pool must degrade to "no capacity
// information" rather than panic inside a request handler.
func (s *resizableSem) Cap() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capacity
}

func (s *resizableSem) InUse() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}

// Waiters reports acquisitions blocked specifically on semaphore capacity.
// Cost-aware candidates may leave their ledger queue before reaching this
// wait, so adaptive demand must observe both sources.
func (s *resizableSem) Waiters() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiters
}

// AcquireAll reserves the whole pool for one exclusive holder — the go-cache
// janitor, which trims a build cache that no heavy stage may be reading.
//
// Semantics, and why exclusivity is a flag rather than "take Cap() slots":
//   - While it is held, TryAcquire and Acquire admit nobody regardless of
//     capacity. A concurrent SetCap that RAISES the cap therefore cannot hand
//     out slots behind the exclusive holder's back; it records the new capacity,
//     which simply governs nothing until exclusivity is released. Counting out
//     Cap() slots could not offer this, because the count is a snapshot.
//   - It never hoards. An attempt succeeds only if the pool is completely idle
//     at that instant; otherwise it holds nothing and retries after poll, so one
//     long compile cannot make the janitor reserve capacity other work could
//     have used.
//   - Releases are never blocked by exclusivity, so in-flight holders always
//     drain and the wait cannot deadlock.
//
// It returns a release func that restores normal admission and wakes waiters
// (idempotent), or false if ctx ends before the pool goes idle.
func (s *resizableSem) AcquireAll(ctx context.Context, poll time.Duration) (func(), bool) {
	if s == nil {
		return nil, false
	}
	for {
		s.mu.Lock()
		if !s.exclusive && s.inUse == 0 && s.priorityWaiters == 0 {
			s.exclusive = true
			s.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					s.mu.Lock()
					s.exclusive = false
					s.broadcastLocked()
					s.mu.Unlock()
				})
			}, true
		}
		s.mu.Unlock()
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
	}
}

// AcquireAllFair first waits without reserving capacity, then (at
// barrierAfter) closes admission for at most barrierMax so continuously
// arriving work cannot starve an over-budget storage sweep forever. The third
// result reports whether the barrier was engaged, including a bounded timeout.
func (s *resizableSem) AcquireAllFair(ctx context.Context, poll, barrierAfter, barrierMax time.Duration) (release func(), acquired, barrierEngaged bool) {
	if s == nil {
		return nil, false, false
	}
	started := time.Now()
	barrierAt := time.Time{}
	defer func() {
		if acquired || !barrierEngaged {
			return
		}
		s.mu.Lock()
		if s.exclusivePending > 0 {
			s.exclusivePending--
		}
		s.broadcastLocked()
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, barrierEngaged
		}
		now := time.Now()
		s.mu.Lock()
		if !barrierEngaged && barrierAfter >= 0 && now.Sub(started) >= barrierAfter {
			s.exclusivePending++
			barrierEngaged = true
			barrierAt = now
			s.broadcastLocked()
		}
		if !s.exclusive && s.inUse == 0 && (barrierEngaged || s.priorityWaiters == 0) {
			if barrierEngaged && s.exclusivePending > 0 {
				s.exclusivePending--
			}
			s.exclusive = true
			s.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					s.mu.Lock()
					s.exclusive = false
					s.broadcastLocked()
					s.mu.Unlock()
				})
			}, true, barrierEngaged
		}
		if barrierEngaged && barrierMax >= 0 && now.Sub(barrierAt) >= barrierMax {
			s.mu.Unlock()
			return nil, false, true
		}
		s.mu.Unlock()
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, barrierEngaged
		case <-timer.C:
		}
	}
}

// admissibleLocked reports whether a new acquisition may take a slot right now.
func (s *resizableSem) admissibleLocked() bool {
	return !s.exclusive && s.exclusivePending == 0 && s.inUse < s.capacity
}

func (s *resizableSem) admissibleReserveLocked(reserve int) bool {
	return !s.exclusive && s.exclusivePending == 0 && s.inUse+max(0, reserve) < s.capacity
}

func (s *resizableSem) broadcastLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}
