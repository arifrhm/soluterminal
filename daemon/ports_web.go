package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func internalPortProcess(p *Port) bool {
	switch strings.ToLower(strings.TrimSuffix(p.Process, ".exe")) {
	case "codex", "claude", "postgres", "mysqld", "redis-server", "sshd":
		return true
	}
	return false
}

// Bounded, credential-free HEAD requests: do not follow redirects or execute
// application actions. Certificates may be self-signed on development servers.
// This transport has no environment proxy and is used only for observed sockets.
func probePortWeb(ctx context.Context, p *Port) string {
	return probePortWebWithTimeout(ctx, p, 350*time.Millisecond)
}

func probePortWebWithTimeout(ctx context.Context, p *Port, timeout time.Duration) string {
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	schemes := []string{"http", "https"}
	httpResponded := false
	if p.Scheme == "https" {
		schemes = []string{"https", "http"}
	}
	for _, scheme := range schemes {
		// Avoid spraying TLS handshakes at unknown TCP/HTTP services during
		// passive startup detection. Explicit checks may try both transports.
		if scheme == "https" && p.Scheme != "https" && !httpResponded && !strings.Contains(strings.ToLower(p.Cmdline), "https") && timeout <= 350*time.Millisecond {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, scheme+"://"+portHost(p)+"/", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if scheme == "http" && resp.StatusCode == http.StatusBadRequest {
				httpResponded = true
				continue
			}
			return scheme
		}
	}
	if httpResponded {
		return "http"
	}
	return ""
}

func (pr *PortRegistry) commitScan(fresh map[string]*Port) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, p := range fresh {
		p.PreviewPath = "/api/proxy/" + strconv.Itoa(int(p.Port)) + "~" + portSelector(p) + "/"
		if internalPortProcess(p) {
			p.WebStatus = "internal"
			p.Scheme = ""
			continue
		}
		// A successful probe is cached for this socket lifetime. Failed startup
		// probes are retried at most twice; explicit checks remain available.
		if p.WebStatus == "ready" {
			continue
		}
		p.WebStatus = "unverified"
		if p.PaneID == "" || p.probeAttempts >= 3 {
			continue
		}
		recheckAfter := time.Duration(p.probeAttempts) * 5 * time.Second
		if time.Since(p.checkedAt) < recheckAfter {
			continue
		}
		wg.Add(1)
		go func(p *Port) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			p.checkedAt = time.Now()
			p.probeAttempts++
			if scheme := probePortWeb(ctx, p); scheme != "" {
				p.Scheme = scheme
				p.WebStatus = "ready"
			} else {
				p.WebStatus = "unverified"
			}
		}(p)
	}
	wg.Wait()
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.commitLocked(fresh)
}

// Discovery alone never sends traffic to unrelated listeners. A user's
// explicit check is bound to this exact process/socket lifetime and expires
// when that listener disappears or is replaced.
func (pr *PortRegistry) Probe(ctx context.Context, port uint16, selector string) (*Port, error) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pr.scanMu.TryLock() {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	defer pr.scanMu.Unlock()
	fresh := pr.snapshot()
	for key, p := range fresh {
		if p.Port != port || selector == "" || portSelector(p) != selector {
			continue
		}
		if internalPortProcess(p) {
			return nil, errors.New("this is an internal tool or TCP service")
		}
		cp := *p
		cp.probeAttempts = 3
		cp.checkedAt = time.Now()
		cp.WebStatus = "unverified"
		if scheme := probePortWebWithTimeout(ctx, &cp, 3*time.Second); scheme != "" {
			cp.Scheme = scheme
			cp.WebStatus = "ready"
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fresh[key] = &cp
		pr.mu.Lock()
		pr.commitLocked(fresh)
		pr.mu.Unlock()
		return &cp, nil
	}
	return nil, errors.New("listener changed; refresh Services and try again")
}
