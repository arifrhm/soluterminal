package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	errRetirementRetained  = errors.New("retirement_material_retained")
	errRetirementDeferred  = errors.New("retirement_deferred")
	errRetirementAuthority = errors.New("retirement_authority_unavailable")
)

type retirementIndex struct {
	Paths      []string
	Tasks      map[string]bool
	Bindings   map[string]retirementMaterialBinding
	Held       map[string]bool
	Promotions map[string]retirementPromotion
	Error      string
}

type retirementMaterial struct {
	mu sync.RWMutex
	// Writer admission precedes TryLock. A failed writer releases admission,
	// so it cannot strand a gate or sleep while holding a partial acquisition.
	writerPending atomic.Bool
	index         atomic.Pointer[retirementIndex]
}

// Bound once by main, before any cleanup starts, even with workflow disabled.
// Explicit authorities on stores/managers make isolated fixtures independent
// of this production binding. No grant or tool allowlist can modify it.
var retirementCleanupAuthority atomic.Pointer[retirementAuthorityStore]

type retirementCleanupContextKey struct{}

type retirementAuthorityContextKey struct{}

func retirementWithAuthority(ctx context.Context, authority *retirementAuthorityStore) context.Context {
	return context.WithValue(ctx, retirementAuthorityContextKey{}, authority)
}

func retirementCleanupContext(ctx context.Context, authority *retirementAuthorityStore, taskID, path string) (context.Context, func(), error) {
	if token, ok := ctx.Value(retirementCleanupContextKey{}).(*retirementCleanupToken); ok {
		if authority != nil && authority.material != token.material {
			return ctx, nil, errRetirementAuthority
		}
		return ctx, func() {}, token.check(taskID, path)
	}
	if authority == nil {
		authority, _ = ctx.Value(retirementAuthorityContextKey{}).(*retirementAuthorityStore)
	}
	if authority == nil {
		authority = retirementCleanupAuthority.Load()
	}
	if authority == nil {
		return ctx, func() {}, nil
	} // isolated, non-daemon helpers
	token, err := authority.material.tryCleanup(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if err := token.check(taskID, path); err != nil {
		token.close()
		return ctx, nil, err
	}
	return context.WithValue(ctx, retirementCleanupContextKey{}, token), token.close, nil
}

func newWfRetirementMaterial() *retirementMaterial {
	m := &retirementMaterial{}
	m.index.Store(&retirementIndex{Tasks: map[string]bool{}, Held: map[string]bool{}})
	return m
}

// A token is passed to nested cleanup instead of recursively acquiring an
// RWMutex (a pending writer would make that pattern deadlock).
type retirementCleanupToken struct {
	material *retirementMaterial
	index    *retirementIndex
	closed   bool
}

func (m *retirementMaterial) tryCleanup(ctx context.Context) (*retirementCleanupToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.writerPending.Load() || !m.mu.TryRLock() {
		return nil, errRetirementDeferred
	}
	// Close the admission race between the first flag read and TryRLock.
	if m.writerPending.Load() {
		m.mu.RUnlock()
		return nil, errRetirementDeferred
	}
	i := m.index.Load()
	if i == nil || i.Error != "" {
		m.mu.RUnlock()
		return nil, errRetirementAuthority
	}
	return &retirementCleanupToken{material: m, index: i}, nil
}

func (t *retirementCleanupToken) close() {
	if t != nil && !t.closed {
		t.closed = true
		t.material.mu.RUnlock()
	}
}

func retirementPathOverlap(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || retirementPathWithin(a, b) || retirementPathWithin(b, a)
}

func retirementPathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (t *retirementCleanupToken) check(taskID, path string) error {
	if t == nil || t.closed {
		return errRetirementAuthority
	}
	if t.index.Tasks[taskID] {
		return errRetirementRetained
	}
	for _, pinned := range t.index.Paths {
		if retirementPathOverlap(path, pinned) {
			return errRetirementRetained
		}
	}
	return nil
}

// Advisory dry-run reporting only. Actual removal still requires a cleanup
// token across final ownership/identity validation and deletion.
func retirementRetainedReason(a *retirementAuthorityStore, taskID, path string) string {
	if a == nil {
		a = retirementCleanupAuthority.Load()
	}
	if a == nil {
		return ""
	}
	i := a.material.index.Load()
	if i == nil || i.Error != "" {
		return errRetirementAuthority.Error()
	}
	if i.Tasks[taskID] {
		return errRetirementRetained.Error()
	}
	for _, pin := range i.Paths {
		if retirementPathOverlap(path, pin) {
			return errRetirementRetained.Error()
		}
	}
	return ""
}
