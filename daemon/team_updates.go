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
// teamUpdatesPath is stamped into the team build only (scripts/release-
// ldflags.sh, from the private team release file). Every public build leaves
// it empty, so a public build has no team source at all: it never polls the
// team channel, and its team-channel installs stay where they are
// (channelSource). If the cloud is unreachable, or answers 404, a team
// install stays on its current version with a clear status; it never falls
// back to the public repo and never changes channel because of it.

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

func teamUpdatesConfigured() bool {
	return teamUpdatesPathRE.MatchString(strings.TrimSpace(teamUpdatesPath))
}

// teamUpdatesBase is "<cloud origin>/<path>/", or "" without a cloud or
// without the stamped endpoint.
func teamUpdatesBase(cloudBase string) string {
	cloudBase = strings.TrimRight(strings.TrimSpace(cloudBase), "/")
	if cloudBase == "" || !teamUpdatesConfigured() {
		return ""
	}
	u, err := url.Parse(cloudBase)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return ""
	}
	// The machine token travels to this base: https only, except a loopback
	// cloud (local development pairing).
	if u.Scheme != "https" && !(u.Scheme == "http" && teamUpdatesLoopbackHost(u.Hostname())) {
		return ""
	}
	return cloudBase + "/" + strings.Trim(strings.TrimSpace(teamUpdatesPath), "/") + "/"
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
func teamUpdatesManifestURL(cloudBase string) string {
	base := teamUpdatesBase(cloudBase)
	if base == "" {
		return ""
	}
	return base + "releases/latest/download/update.json"
}

// teamAssetURL maps a signed manifest's asset URL
// (https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>) onto the
// cloud endpoint, keeping the tag (a carried installer can live under an
// older tag) and the asset name. ok is false for any other URL shape.
func teamAssetURL(raw, cloudBase string) (string, bool) {
	base := teamUpdatesBase(cloudBase)
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
	if token == "" || !onTeamUpdatesEndpoint(req.URL, teamUpdatesBase(base)) {
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
