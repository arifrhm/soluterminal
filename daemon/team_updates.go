package main

// Team updates over the cloud.
//
// The team build's releases live in a private repo, so a team install cannot
// download them anonymously. The SoluTerminal cloud serves them instead, to
// team machines only (a valid machine token whose account the cloud confirms
// as team; everyone else gets 404). The cloud mirrors GitHub's release
// layout under its endpoint:
//
//	<cloud>/<teamUpdatesPath>/releases/latest/download/update.json
//	<cloud>/<teamUpdatesPath>/releases/download/<tag>/<asset>
//
// so the updater's resolve-then-pin flow, its control file and its rollback
// all work unchanged on the team channel. The cloud is only a transport: the
// manifest is still Ed25519-verified and must still be signed for the team
// channel (update_flavor.go), and every binary is checked against the
// signed SHA-256, wherever it came from.
//
// The machine token is attached only to requests for that endpoint on the
// cloud this daemon is paired with (teamUpdatesAuth), never to GitHub or any
// other host.
//
// Where the endpoint path comes from (teamUpdatesPathFor):
//
//  1. The team build has it stamped (teamUpdatesPath, scripts/release-
//     ldflags.sh, from the private team release file) and always uses it.
//  2. A public build contains no team path at all. It learns the path only
//     from the paired cloud's own answer about this machine: GET
//     /api/entitlements with the machine token names "teamUpdates" only when
//     that cloud confirms the machine is on a team (entitlements.go caches it
//     with the verdict and the cloud origin it came from). The path is
//     validated with the same rule as the stamp and only ever resolved
//     against that same cloud's origin; a full URL is never accepted, and a
//     cached path stops counting once the machine is paired to another
//     cloud, its verdict is no longer team, or it is older than
//     entitlementsTeamUpdatesMaxAge.
//
// Without either, the build has no team source: it never polls the team
// channel, and an install whose account is team stays on public until the
// cloud names the endpoint (channelSource). If the cloud is unreachable, or
// answers 404, a team install stays on its current version with a clear
// status; it never falls back to the public repo and never changes channel
// because of it.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// teamUpdatesPath is the cloud's team update endpoint path, stamped with
// -ldflags "-X main.teamUpdatesPath=..." for the team flavour only.
var teamUpdatesPath = ""

var teamUpdatesPathRE = regexp.MustCompile(`^/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*/?$`)

// validTeamUpdatesPath reports whether p is an endpoint path the token may
// be sent to: an absolute path of plain segments (no scheme, host, query,
// "..", or empty segment).
func validTeamUpdatesPath(p string) bool {
	return teamUpdatesPathRE.MatchString(strings.TrimSpace(p))
}

// teamUpdatesConfigured reports that this build has the endpoint stamped in
// (the team build).
func teamUpdatesConfigured() bool {
	return validTeamUpdatesPath(teamUpdatesPath)
}

// teamUpdatesOrigin is cloudBase's origin ("scheme://host[:port]", lower
// case) when the machine token may travel to it: https, or http to a
// loopback cloud (local development pairing). "" for anything else,
// including a base with a path or query.
func teamUpdatesOrigin(cloudBase string) string {
	cloudBase = strings.TrimRight(strings.TrimSpace(cloudBase), "/")
	if cloudBase == "" {
		return ""
	}
	u, err := url.Parse(cloudBase)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && teamUpdatesLoopbackHost(u.Hostname())) {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// teamUpdatesBase is "<cloud origin>/<path>/", or "" without a usable cloud
// or without a valid endpoint path.
func teamUpdatesBase(cloudBase, path string) string {
	if !validTeamUpdatesPath(path) {
		return ""
	}
	origin := teamUpdatesOrigin(cloudBase)
	if origin == "" {
		return ""
	}
	return origin + "/" + strings.Trim(strings.TrimSpace(path), "/") + "/"
}

func teamUpdatesLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// teamUpdatesManifestURL is the team channel's latest-alias manifest URL on
// cloudBase, or "".
func teamUpdatesManifestURL(cloudBase, path string) string {
	base := teamUpdatesBase(cloudBase, path)
	if base == "" {
		return ""
	}
	return base + "releases/latest/download/update.json"
}

// teamAssetURL maps a signed manifest's asset URL
// (https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>) onto the
// cloud endpoint, keeping the tag (a carried installer can live under an
// older tag) and the asset name. ok is false for any other URL shape.
func teamAssetURL(raw, cloudBase, path string) (string, bool) {
	base := teamUpdatesBase(cloudBase, path)
	if base == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", false
	}
	tag, name := parts[4], parts[5]
	if tag == "" || name == "" || tag == "." || tag == ".." || name == "." || name == ".." {
		return "", false
	}
	return base + "releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name), true
}

// teamUpdatesAuth attaches the machine token to requests for the team update
// endpoint of the paired cloud, and to nothing else.
type teamUpdatesAuth struct {
	next  http.RoundTripper
	creds func() (base, token string)
	// path is the endpoint path for the paired cloud base (the updater's
	// teamUpdatesPathFor). Nil means the stamped path only.
	path func(base string) string
}

func (t *teamUpdatesAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	if t.creds == nil {
		return next.RoundTrip(req)
	}
	base, token := t.creds()
	path := teamUpdatesPath
	if t.path != nil {
		path = t.path(base)
	}
	if token == "" || !onTeamUpdatesEndpoint(req.URL, teamUpdatesBase(base, path)) {
		return next.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return next.RoundTrip(clone)
}

func onTeamUpdatesEndpoint(target *url.URL, base string) bool {
	if base == "" || target == nil {
		return false
	}
	b, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(target.Scheme, b.Scheme) && strings.EqualFold(target.Host, b.Host) &&
		strings.HasPrefix(target.Path, b.Path) && !strings.Contains(target.Path, "/../")
}

// updateHTTPError is a non-200 answer for an update asset.
type updateHTTPError struct {
	label string
	code  int
}

func (e *updateHTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.label, e.code) }

// errTeamUpdatesNoCloud: the team channel's transport needs the paired cloud.
var errTeamUpdatesNoCloud = errors.New("team updates come through the SoluTerminal cloud, and this computer is not connected to it; staying on this version")

// errTeamUpdatesNoSource: paired, but this build has no endpoint path for
// the paired cloud (a public build whose cloud has not named one in a team
// answer, or named it while paired to another cloud).
var errTeamUpdatesNoSource = errors.New("team updates unavailable: the SoluTerminal cloud this computer is connected to has not confirmed it as a team machine yet; staying on this version")

// teamUpdatesError turns the cloud's answers into a status a person can act
// on. The cloud answers 404 to anyone it does not serve, so a 404 on the
// latest alias means this machine was not confirmed as a team machine (or no
// team release exists yet). Either way the install stays where it is.
func teamUpdatesError(err error) error {
	var he *updateHTTPError
	if err == nil || !errors.As(err, &he) {
		return err
	}
	switch {
	case he.code == http.StatusNotFound && !isTransientUpdateErr(err):
		return fmt.Errorf("team updates unavailable: the SoluTerminal cloud has no team update for this computer (it is not confirmed as a team machine, or no team release exists yet); staying on this version (%w)", err)
	case he.code == http.StatusServiceUnavailable:
		return transient(fmt.Errorf("team updates unavailable: the SoluTerminal cloud's team update service is not running; staying on this version (%w)", err))
	}
	return err
}
