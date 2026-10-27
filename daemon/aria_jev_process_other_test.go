//go:build !linux && !windows

package main

import (
	"context"
	"errors"
	"testing"
)

func TestJevProcessOtherUnsupportedNeverFabricatesIdentity(t *testing.T) {
	identity, err := probeJevProcessIdentity(context.Background(), 100)
	if !errors.Is(err, errJevProcessUnsupported) || identity != (jevProcessIdentity{}) {
		t.Fatalf("unsupported probe returned %+v %v", identity, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeJevProcessIdentity(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
