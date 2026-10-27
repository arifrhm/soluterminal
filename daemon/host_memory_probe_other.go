//go:build !linux

package main

import (
	"context"
	"time"
)

type windowsHostProbe struct{}

func newWfWindowsHostProbe() *windowsHostProbe           { return nil }
func (*windowsHostProbe) Start(context.Context)          {}
func (*windowsHostProbe) Refresh(context.Context)        {}
func (*windowsHostProbe) Sample(time.Time) hostMemSample { return hostMemSample{Status: "n/a"} }
