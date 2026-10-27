package main

import (
	"time"
)

func quotaWakeFresh(now, observed time.Time, maxAge time.Duration) bool {
	return !observed.IsZero() && !observed.After(now.Add(time.Minute)) && now.Sub(observed) <= maxAge
}
