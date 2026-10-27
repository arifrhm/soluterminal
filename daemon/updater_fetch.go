package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"

	"soluterminal-shared/releases"
)

// getReleaseAsset GETs assetURL with client. A 404 under the current releases
// repo is retried once under the legacy owner, because the repo may not have
// moved yet; after the move GitHub redirects the legacy URLs instead. Nothing
// fetched either way is trusted until it passes the manifest signature (or,
// for binaries, the SHA-256 from the signed manifest).
//
// Errors name only the asset (for example "update.json"), never the URL: they
// reach the version popover and the fleet summary. The full URL goes to the
// daemon log.
func getReleaseAsset(ctx context.Context, client *http.Client, assetURL string) (*http.Response, error) {
	resp, err := getReleaseAssetOnce(ctx, client, assetURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		if legacy, ok := releases.LegacyFallback(assetURL); ok {
			resp.Body.Close()
			slog.Info("update asset not found under the current releases repo; trying the legacy owner", "url", assetURL)
			return getReleaseAssetOnce(ctx, client, legacy)
		}
	}
	return resp, nil
}

func getReleaseAssetOnce(ctx context.Context, client *http.Client, assetURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid update URL", updateAssetLabel(assetURL))
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Info("update asset fetch failed", "url", assetURL, "err", err)
		return nil, fmt.Errorf("%s: %w", updateAssetLabel(assetURL), withoutURL(err))
	}
	if resp.StatusCode != http.StatusOK {
		slog.Info("update asset fetch status", "url", assetURL, "status", resp.StatusCode)
	}
	return resp, nil
}

// updateAssetLabel is the file name at the end of rawURL ("update.json",
// "solud-linux-amd64"), or "update server" when there is none.
func updateAssetLabel(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "update server"
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "update server"
	}
	return name
}

// withoutURL drops the "Get \"<url>\":" prefix net/http puts on transport
// errors while keeping the cause (timeouts, DNS) for errors.Is and the log.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

var updateErrorURLRE = regexp.MustCompile(`https?://[^\s"'<>]+`)

// redactUpdateURLs replaces every URL in a user-facing updater message with
// its file name. A backstop for errors built outside the helpers above.
func redactUpdateURLs(s string) string {
	return updateErrorURLRE.ReplaceAllStringFunc(s, updateAssetLabel)
}
