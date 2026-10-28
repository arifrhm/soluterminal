package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"soluterminal-shared/releases"
)

// The installer half of the team migration. Observed 2026-10-28: a public
// 0.0.1225 desktop app on a team machine whose daemons had moved to the team
// channel and to team 0.0.1226. The app's own updater (desktop/src-tauri
// shell_update_manifest.rs) reads only the public repo and refuses anything
// but a public manifest, so the team installer has to come from the local
// daemon: it follows the team channel only on a positive team verdict,
// fetches through the cloud with the machine token, and accepts only a
// manifest signed "flavor": "team" and bytes matching its signed SHA-256.

const teamShellInstallerName = "soluterminal_3.0.0_x64-setup.exe"

// serveShellRelease publishes a signed manifest of flavor for version under
// releasesBase, whose windows-amd64 installer is installerURL with payload's
// digest.
func serveShellRelease(bodies map[string][]byte, priv ed25519.PrivateKey, releasesBase, flavor, version, installerURL string, payload []byte) []byte {
	sum := sha256.Sum256(payload)
	body := []byte(`{"flavor":"` + flavor + `","version":"` + version + `","platforms":{"` + platformKeyCurrent() +
		`":{"url":"https://cdn.test/solud-` + version + `","sha256":"abc"}},"shell":{"version":"` + version +
		`","platforms":{"windows-amd64":{"url":"` + installerURL + `","sha256":"` + hex.EncodeToString(sum[:]) + `"}}}}`)
	bodies[releasesBase+"latest/download/update.json"] = body
	pinned := releasesBase + "download/v" + version + "/"
	bodies[pinned+"update.json"] = body
	bodies[pinned+"update.json.sig"] = signB64(priv, body)
	return body
}

type shellRig struct {
	*publicTeamRig
	payload  []byte
	signed   string // the installer URL the team manifest names
	launched []string
}

// teamInstallerRig is publicBuildOnTeamMachine with an installed public
// shell 1.0.0 and a team release 3.0.0 that carries a team installer.
func teamInstallerRig(t *testing.T) *shellRig {
	t.Helper()
	r := &shellRig{publicTeamRig: publicBuildOnTeamMachine(t), payload: []byte("team NSIS installer bytes")}
	r.signed = "https://github.com/owner/team-releases/releases/download/v3.0.0/" + teamShellInstallerName
	serveShellRelease(r.cloud.files.bodies, r.priv, r.team, releases.FlavorTeam, "3.0.0", r.signed, r.payload)
	r.cloud.files.bodies[r.team+"download/v3.0.0/"+teamShellInstallerName] = r.payload
	r.u.shellCurrent = "1.0.0"
	r.u.shellDisplayPath = func(path, _ string) (string, error) { return path, nil }
	r.u.launchShell = func(path, _ string, _ *shellLaunchLog) (launchResult, error) {
		r.launched = append(r.launched, path)
		return launchResult{Method: "test"}, nil
	}
	return r
}

func (r *shellRig) nameTheTeamEndpoint(t *testing.T) {
	t.Helper()
	r.cloud.answer(testTeamCloud, `{"team":true,"tier":"team","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	r.cloud.requests()
}

// assertNoTeamRequest fails if anything reached the team endpoint or carried
// a token.
func (r *shellRig) assertNoTeamRequest(t *testing.T) {
	t.Helper()
	urls, auth := r.cloud.requests()
	for _, u := range urls {
		if strings.HasPrefix(u, r.team) || auth[u] != "" {
			t.Fatalf("public channel requested %s (Authorization %q)", u, auth[u])
		}
	}
}

func TestPublicAppGetsTheTeamInstallerOnlyThroughTheDaemonsVerifiedTeamManifest(t *testing.T) {
	r := teamInstallerRig(t)
	dataDir := t.TempDir()

	// Until the cloud confirms the machine, the daemon follows public and the
	// team installer is out of reach.
	st := r.check(t, releases.FlavorPublic, "2.0.0")
	if st.ShellLatest == "3.0.0" {
		t.Fatalf("public channel offered the team installer: %+v", st)
	}
	_, _ = r.u.ApplyShell(context.Background(), dataDir, false)
	r.assertNoTeamRequest(t)
	if len(r.launched) != 0 {
		t.Fatalf("launched %v on the public channel", r.launched)
	}

	r.nameTheTeamEndpoint(t)
	st = r.check(t, releases.FlavorTeam, "3.0.0")
	if !st.ShellAvailable || st.ShellLatest != "3.0.0" || st.UpdateChannel != releases.FlavorTeam {
		t.Fatalf("team channel did not offer the team installer: %+v", st)
	}
	path, err := r.u.ApplyShell(context.Background(), dataDir, false)
	if err != nil {
		t.Fatalf("ApplyShell: %v", err)
	}
	if want := filepath.Join(dataDir, "shell-update", teamShellInstallerName); path != want || len(r.launched) != 1 || r.launched[0] != want {
		t.Fatalf("staged %q launched %v, want %s", path, r.launched, want)
	}
	if got, _ := os.ReadFile(path); string(got) != string(r.payload) {
		t.Fatalf("staged %q", got)
	}
	// Every byte came through the paired cloud's endpoint with the machine
	// token, never from GitHub.
	r.assertOnlyTeam(t)

	// Bytes that do not match the signed digest are never launched.
	r.launched = nil
	r.cloud.files.bodies[r.team+"download/v3.0.0/"+teamShellInstallerName] = []byte("tampered installer")
	if _, err := r.u.ApplyShell(context.Background(), dataDir, false); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("tampered installer: %v", err)
	}
	if len(r.launched) != 0 {
		t.Fatalf("launched a tampered installer: %v", r.launched)
	}
}

// A machine whose account is not on a team never gets the team installer,
// even when the cloud's answer names the team endpoint.
func TestPublicMachineNeverGetsTheTeamInstaller(t *testing.T) {
	r := teamInstallerRig(t)
	publicInstaller := r.public + "download/v2.0.0/soluterminal_2.0.0_x64-setup.exe"
	publicPayload := []byte("public NSIS installer bytes")
	serveShellRelease(r.cloud.files.bodies, r.priv, r.public, releases.FlavorPublic, "2.0.0", publicInstaller, publicPayload)
	r.cloud.files.bodies[publicInstaller] = publicPayload

	time.Sleep(2 * time.Millisecond) // a strictly newer verdict than the cached one
	r.cloud.answer(testTeamCloud, `{"team":false,"tier":"account","teamUpdates":"`+cloudNamedTeamPath+`"}`)
	r.e.refresh(context.Background(), "")
	r.cloud.requests()

	for i := 0; i < 3; i++ {
		st := r.check(t, releases.FlavorPublic, "2.0.0")
		if st.ShellLatest != "2.0.0" {
			t.Fatalf("public machine: %+v", st)
		}
	}
	path, err := r.u.ApplyShell(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatalf("ApplyShell: %v", err)
	}
	if filepath.Base(path) != "soluterminal_2.0.0_x64-setup.exe" || len(r.launched) != 1 {
		t.Fatalf("public machine staged %q launched %v", path, r.launched)
	}
	r.assertNoTeamRequest(t)
}

// On the team channel, a manifest that is not a verified team manifest
// stages nothing: one signed for the public build, and one changed after
// signing.
func TestTeamChannelStagesNoInstallerFromAnUnverifiedManifest(t *testing.T) {
	for name, tamper := range map[string]func(r *shellRig){
		"public flavour": func(r *shellRig) {
			serveShellRelease(r.cloud.files.bodies, r.priv, r.team, releases.FlavorPublic, "3.0.0", r.signed, r.payload)
		},
		"changed after signing": func(r *shellRig) {
			pinned := r.team + "download/v3.0.0/update.json"
			changed := strings.Replace(string(r.cloud.files.bodies[pinned]), "github.com/owner/team-releases", "github.com/attacker/releases", 1)
			r.cloud.files.bodies[pinned] = []byte(changed)
		},
		"signed by another key": func(r *shellRig) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				panic(err)
			}
			serveShellRelease(r.cloud.files.bodies, other, r.team, releases.FlavorTeam, "3.0.0", r.signed, r.payload)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := teamInstallerRig(t)
			tamper(r)
			r.nameTheTeamEndpoint(t)
			if _, err := r.u.Check(context.Background()); err == nil {
				t.Fatal("an unverified team manifest was accepted")
			}
			if st := r.u.Status(); st.ShellAvailable || st.ShellLatest == "3.0.0" {
				t.Fatalf("offered the installer: %+v", st)
			}
			if _, err := r.u.ApplyShell(context.Background(), t.TempDir(), false); err == nil {
				t.Fatal("staged an installer without a verified team manifest")
			}
			if len(r.launched) != 0 {
				t.Fatalf("launched %v", r.launched)
			}
			urls, _ := r.cloud.requests()
			for _, u := range urls {
				if strings.HasSuffix(u, ".exe") {
					t.Fatalf("downloaded %s", u)
				}
			}
		})
	}
}
