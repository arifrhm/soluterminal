package main

import (
	"net/http"
	"os"
	"strings"
)

// Client-visible configuration. Mirrors cloud/main.go's /api/config
// shape so the same CloudGate.tsx front-end flow drives both surfaces —
// the only differentiator is isLocal=true, which tells the frontend to
// skip the machine-picker step (there's exactly one machine in local
// mode: this one).
//
// Values come from two sources, in order:
//
//  1. Runtime env vars (loaded by loadEnvFile from ~/.solu/env on startup).
//     Intended for development and for advanced users who want to point
//     their local daemon at a different Supabase project.
//
//  2. Link-time defaults baked via -ldflags "-X main.defaultSupaURL=…
//     -X main.defaultSupaAnonKey=…" — set up in publish.sh. This is how
//     shipped binaries carry the team's Supabase project so testers can
//     install + log in without configuring anything locally.
//
// If neither source provides both values, /api/config returns 404. That
// preserves the legacy "no /api/config → local-passthrough, no auth"
// behavior for developers running a raw `go run ./daemon` build with no
// credentials configured. Production/shipped binaries always have the
// ldflag defaults, so for testers the auth gate is effectively mandatory.
var (
	defaultSupaURL     string
	defaultSupaAnonKey string
)

func resolvedSupaCreds() (url string, anonKey string) {
	url = strings.TrimSpace(os.Getenv("SOLU_SUPABASE_URL"))
	anonKey = strings.TrimSpace(os.Getenv("SOLU_SUPABASE_ANON_KEY"))
	if url == "" {
		url = strings.TrimSpace(defaultSupaURL)
	}
	if anonKey == "" {
		anonKey = strings.TrimSpace(defaultSupaAnonKey)
	}
	return
}

func (a *apiServer) handleConfig(w http.ResponseWriter, _ *http.Request) {
	url, anonKey := resolvedSupaCreds()
	if url == "" || anonKey == "" {
		// 404 — intentional. The frontend interprets a missing /api/config
		// as "this daemon wasn't built with auth; stay in local-passthrough
		// mode." Never half-answer: a partial payload would leave the
		// frontend in a broken cloud-mode-without-Supabase state.
		http.NotFound(w, nil)
		return
	}
	writeJSON(w, 200, map[string]any{
		"supabaseUrl":     url,
		"supabaseAnonKey": anonKey,
		// isLocal=true is the signal that skips MachinePicker in
		// CloudGate.tsx. Cloud's /api/config (cloud/main.go) never sets
		// this, so the normal cloud-relay flow is unaffected.
		"isLocal": true,
		// Public beta: the local app works without an account (the loopback
		// and token gate in auth.go protect this API, not Supabase). Set
		// SOLU_ACCOUNT_REQUIRED=1 to restore the mandatory sign-in.
		"accountOptional": !envTruthy("SOLU_ACCOUNT_REQUIRED"),
		"authProviders":   authProvidersFromEnv(),
	})
}

// authProvidersFromEnv lists extra Supabase sign-in providers the project has
// enabled (SOLU_AUTH_PROVIDERS=google). Empty, the default, means email only;
// Google stays off until its consent screen is SoluTerminal-branded.
func authProvidersFromEnv() []string {
	out := []string{}
	for _, p := range strings.Split(os.Getenv("SOLU_AUTH_PROVIDERS"), ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
