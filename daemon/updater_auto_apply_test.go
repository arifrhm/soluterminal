package main

import (
	"context"
	"errors"
	"testing"
)

func TestRunAutoApplyIfEligible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		enabled   bool
		status    *UpdateStatus
		wantCalls int
	}{
		{name: "disabled preference", status: &UpdateStatus{Available: true}},
		{name: "enabled preference", enabled: true, status: &UpdateStatus{Available: true}, wantCalls: 1},
		{name: "required overrides preference", status: &UpdateStatus{Available: true, Required: true}, wantCalls: 1},
		{name: "no update", enabled: true, status: &UpdateStatus{}},
		{name: "already applying", enabled: true, status: &UpdateStatus{Available: true, Applying: true}},
		{name: "nil status", enabled: true, status: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := runAutoApplyIfEligible(context.Background(), tt.enabled, tt.status, func(context.Context) error {
				calls++
				return nil
			})
			if err != nil {
				t.Fatalf("runAutoApplyIfEligible: %v", err)
			}
			if calls != tt.wantCalls {
				t.Fatalf("apply calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestRunAutoApplyIfEligibleReturnsApplyError(t *testing.T) {
	t.Parallel()
	want := errors.New("apply failed")
	err := runAutoApplyIfEligible(
		context.Background(),
		true,
		&UpdateStatus{Available: true},
		func(context.Context) error { return want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
