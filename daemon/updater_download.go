package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const defaultMaxUpdateDownloadBytes int64 = 128 << 20

// The binary download must survive a slow-but-steady connection. The fixed
// 20s http.Client timeout used for manifest/control requests cut a ~43MB
// release off mid-body on anything slower than ~17 Mbps, which is common on
// home links. Bound the download by progress instead: abort only after
// downloadIdleTimeout with no bytes received, with downloadMaxDuration as the
// absolute ceiling.
const (
	defaultUpdateDownloadIdleTimeout = 30 * time.Second
	defaultUpdateDownloadMaxDuration = 15 * time.Minute
)

var maxUpdateDownloadBytes = defaultMaxUpdateDownloadBytes

// download streams the URL into dstPath, computing SHA-256 as it goes,
// and fails if the hash doesn't match wantHex. Writes to dstPath+".tmp"
// first and renames into place, so a partial download can never leave
// a corrupted file the atomic swap would then promote.
func (u *Updater) download(ctx context.Context, url, wantHex, dstPath string) error {
	idle := u.downloadIdleTimeout
	if idle <= 0 {
		idle = defaultUpdateDownloadIdleTimeout
	}
	maxDuration := u.downloadMaxDuration
	if maxDuration <= 0 {
		maxDuration = defaultUpdateDownloadMaxDuration
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	// On the team channel the signed manifest names the private repo's
	// GitHub URLs; the bytes come through the cloud instead (team_updates.go).
	// The SHA-256 check below is unchanged.
	if u.teamViaCloudNow() {
		base, path := u.teamUpdatesSource()
		if base == "" || path == "" {
			return u.teamUpdatesUnavailable()
		}
		if mapped, ok := teamAssetURL(url, base, path); ok {
			url = mapped
		}
	}

	// Use the configured transport but WITHOUT the short total timeout: the
	// idle watchdog below bounds a stalled body, and the transport's
	// ResponseHeaderTimeout bounds the pre-header phase. A single total
	// deadline cannot distinguish "slow" from "stuck" on a multi-megabyte file.
	client := &http.Client{Transport: u.httpClient.Transport}
	resp, err := getReleaseAsset(ctx, client, url)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("download %s: HTTP %d", updateAssetLabel(url), resp.StatusCode)
	}
	if maxUpdateDownloadBytes > 0 && resp.ContentLength > maxUpdateDownloadBytes {
		resp.Body.Close()
		return fmt.Errorf("download %s: content length %d exceeds cap %d", updateAssetLabel(url), resp.ContentLength, maxUpdateDownloadBytes)
	}

	body := newIdleTimeoutBody(resp.Body, idle, cancel)
	defer body.Close()

	tmpPath := dstPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	var reader io.Reader = body
	if maxUpdateDownloadBytes > 0 {
		reader = &io.LimitedReader{R: body, N: maxUpdateDownloadBytes + 1}
	}
	n, err := io.Copy(io.MultiWriter(f, h), reader)
	cerr := f.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		if body.timedOut.Load() {
			return fmt.Errorf("copy after %d bytes: stalled (no data for %s)", n, idle)
		}
		return fmt.Errorf("copy after %d bytes: %w", n, err)
	}
	if cerr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close: %w", cerr)
	}
	if maxUpdateDownloadBytes > 0 && n > maxUpdateDownloadBytes {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("download %s: exceeded cap %d", url, maxUpdateDownloadBytes)
	}

	gotHex := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(gotHex, wantHex) {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sha256 mismatch: want %s got %s", wantHex, gotHex)
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s → %s: %w", tmpPath, dstPath, err)
	}
	return nil
}

// idleTimeoutBody cancels the in-flight request when no bytes have arrived for
// the idle window. Every successful Read resets the timer, so a slow but
// steady transfer may take as long as it needs while a genuinely stalled
// connection is cut promptly (a plain http.Client.Timeout cannot tell the two
// apart and instead caps the whole transfer).
type idleTimeoutBody struct {
	body     io.ReadCloser
	idle     time.Duration
	cancel   context.CancelFunc
	timer    *time.Timer
	timedOut atomic.Bool
}

func newIdleTimeoutBody(body io.ReadCloser, idle time.Duration, cancel context.CancelFunc) *idleTimeoutBody {
	b := &idleTimeoutBody{body: body, idle: idle, cancel: cancel}
	b.timer = time.AfterFunc(idle, func() {
		b.timedOut.Store(true)
		cancel()
	})
	return b
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		b.timer.Stop()
		return n, err
	}
	b.timer.Reset(b.idle)
	return n, nil
}

func (b *idleTimeoutBody) Close() error {
	b.timer.Stop()
	return b.body.Close()
}
