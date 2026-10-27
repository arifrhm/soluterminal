package main

import (
	"context"
	"sync"
)

type fakeHostStorageSource struct {
	mu     sync.Mutex
	sample hostStorageSample
	calls  int
}

func (f *fakeHostStorageSource) Sample(context.Context) hostStorageSample {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.sample
}

func (f *fakeHostStorageSource) Latest() (hostStorageSample, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sample, true
}

func (f *fakeHostStorageSource) Refresh(context.Context) hostStorageSample {
	return f.Sample(context.Background())
}

func (f *fakeHostStorageSource) setFreeMB(mb int64) {
	f.mu.Lock()
	f.sample.FreeBytes = mb * 1024 * 1024
	f.mu.Unlock()
}

func hostPressureEngine(source hostStorageSource, guestFreeMB int64) *hostStorageService {
	return &hostStorageService{hostStorage: source, diskProbe: func(string) (int64, bool) { return guestFreeMB * 1024 * 1024, true }}
}
