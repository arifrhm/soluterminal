package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOwnerRetirementViewerAcknowledgementBindsAllConnections(t *testing.T) {
	p := &Pane{subs: map[int64]*subEntry{1: {ctrl: make(chan []byte, 8)}, 2: {ctrl: make(chan []byte, 8)}}}
	type answer struct {
		lease *ownerViewerLease
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		lease, err := p.beginOwnerViewerLease(context.Background(), "exact")
		done <- answer{lease, err}
	}()
	for _, sub := range p.subs {
		var probe map[string]any
		if err := json.Unmarshal(<-sub.ctrl, &probe); err != nil || probe["token"] != "exact" {
			t.Fatal("missing exact probe")
		}
	}
	p.acknowledgeOwnerRetirement(1, "stale", true, "")
	p.acknowledgeOwnerRetirement(1, "exact", true, "")
	select {
	case <-done:
		t.Fatal("one ACK retired two viewers")
	default:
	}
	p.acknowledgeOwnerRetirement(2, "exact", true, "")
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	p.subsMu.Lock()
	p.ownerViewerRevision++
	p.subs[3] = &subEntry{ctrl: make(chan []byte, 8)}
	p.subsMu.Unlock()
	p.ownerRetirementGate.Lock()
	err := p.ownerViewersValidLocked(result.lease, true)
	p.ownerRetirementGate.Unlock()
	if err == nil || !strings.Contains(err.Error(), "connections changed") {
		t.Fatalf("new viewer was accepted: %v", err)
	}
	p.releaseOwnerViewers(result.lease)
}
func TestOwnerRetirementViewerPendingInputCancelsAndReleases(t *testing.T) {
	for _, mode := range []string{"nack", "input", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			sub := &subEntry{ctrl: make(chan []byte, 8)}
			p := &Pane{subs: map[int64]*subEntry{1: sub}}
			done := make(chan error, 1)
			go func() { _, err := p.beginOwnerViewerLease(context.Background(), "exact"); done <- err }()
			<-sub.ctrl
			switch mode {
			case "nack":
				p.acknowledgeOwnerRetirement(1, "exact", false, "draft")
			case "input":
				if err := p.ownerRetirementInputBarrier(); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				p.subsMu.Lock()
				delete(p.subs, 1)
				p.ownerViewerRevision++
				p.subsMu.Unlock()
				p.acknowledgeOwnerRetirement(1, "exact", true, "")
			}
			if err := <-done; err == nil {
				t.Fatal("pending interaction accepted")
			}
			if mode != "disconnect" {
				var release map[string]any
				json.Unmarshal(<-sub.ctrl, &release)
				if release["type"] != "owner-retirement-release" {
					t.Fatal("client pause not released")
				}
			}
		})
	}
}
func TestOwnerRetirementUnknownViewerTimesOutWithoutClose(t *testing.T) {
	p := &Pane{subs: map[int64]*subEntry{1: {ctrl: make(chan []byte, 8)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := p.beginOwnerViewerLease(ctx, "unknown"); err == nil {
		t.Fatal("unknown viewer accepted")
	}
	if p.ownerViewerLease != nil || p.ownerRetirementCommitted {
		t.Fatal("timed out viewer retained a gate")
	}
	if err := p.ownerRetirementInputBarrier(); err != nil {
		t.Fatal("timeout blocked ordinary input")
	}
}

func TestOwnerRetirementDoesNotForgetDisconnectedDraft(t *testing.T) {
	p := &Pane{Role: paneRoleTaskOwner, ownerViewerKnowledgeKnown: true, ownerViewerPendingDevices: map[string]bool{"viewer:phone": true}, subs: map[int64]*subEntry{}}
	if _, err := p.beginOwnerViewerLease(context.Background(), "first"); err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("forgot offline draft: %v", err)
	}
	sub := &subEntry{ctrl: make(chan []byte, 8), retirementViewerID: "phone"}
	p.subsMu.Lock()
	p.subs[1] = sub
	p.ownerViewerRevision++
	p.subsMu.Unlock()
	done := make(chan *ownerViewerLease, 1)
	go func() {
		lease, err := p.beginOwnerViewerLease(context.Background(), "second")
		if err != nil {
			done <- nil
		} else {
			done <- lease
		}
	}()
	<-sub.ctrl
	p.acknowledgeOwnerRetirement(1, "second", true, "")
	lease := <-done
	if lease == nil {
		t.Fatal("same device could not resolve its draft uncertainty")
	}
	p.releaseOwnerViewers(lease)
	if !p.ownerViewerPendingDevices["viewer:phone"] {
		t.Fatal("released client may draft again but was still considered clean")
	}
	p.subsMu.Lock()
	delete(p.subs, 1)
	p.ownerViewerRevision++
	p.subsMu.Unlock()
	if _, err := p.beginOwnerViewerLease(context.Background(), "third"); err == nil {
		t.Fatal("disconnect erased reactivated draft possibility")
	}
}

func TestOwnerRetirementSiblingTabCannotSettleDisconnectedPage(t *testing.T) {
	p := &Pane{Role: paneRoleTaskOwner, ownerViewerKnowledgeKnown: true, ownerViewerPendingDevices: map[string]bool{"viewer:lost-tab": true}, subs: map[int64]*subEntry{2: {ctrl: make(chan []byte, 8), deviceID: "same-browser", retirementViewerID: "other-tab"}}}
	if _, err := p.beginOwnerViewerLease(context.Background(), "attempt"); err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("sibling tab could attest lost page: %v", err)
	}
}
func TestOwnerRetirementCleanClosePersistsOnlyAfterExactDisconnect(t *testing.T) {
	for _, mode := range []string{"clean", "later-input", "sibling-page"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			p := &Pane{Role: paneRoleTaskOwner, ownerViewerKnowledgeKnown: true, buffer: NewRingBuffer(1024), subs: map[int64]*subEntry{}, onOwnerViewersCommit: func(*Pane) error { writes++; return nil }}
			stream := p.subscribeStream(80, 24, subModeFull, 0, subOptions{deviceID: "shared", retirementViewerID: "first-page"})
			if writes != 1 || !p.ownerViewerPendingDevices["viewer:first-page"] {
				t.Fatal("new viewer uncertainty not persisted")
			}
			var sibling *subStream
			if mode == "sibling-page" {
				sibling = p.subscribeStream(80, 24, subModeFull, 0, subOptions{deviceID: "shared", retirementViewerID: "second-page"})
				defer sibling.cancel()
			}
			p.ownerViewerClosing(stream.id, true)
			if !p.ownerViewerPendingDevices["viewer:first-page"] {
				t.Fatal("advisory close cleared a still-live connection")
			}
			if mode == "later-input" {
				p.ownerViewerClosing(stream.id, false)
			}
			stream.cancel()
			if mode == "later-input" {
				if !p.ownerViewerPendingDevices["viewer:first-page"] {
					t.Fatal("input after clean-close attestation was forgotten")
				}
			} else if p.ownerViewerPendingDevices["viewer:first-page"] {
				t.Fatal("clean disconnected page permanently blocked cleanup")
			}
			if mode == "sibling-page" && !p.ownerViewerPendingDevices["viewer:second-page"] {
				t.Fatal("closing one page cleared another page's uncertainty")
			}
		})
	}
}
