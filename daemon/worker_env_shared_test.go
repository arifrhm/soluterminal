package main

import (
	"strings"
)

// Secrets the allowlist must never carry, on any OS. Named after what workers
// on this machine could actually leak.
var workerEnvSecretNames = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "SOLU_SUPABASE_ANON_KEY",
	"OPENAI_API_KEY", "NPM_TOKEN",
}

func envValuesForKey(env []string, key string) []string {
	var values []string
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			values = append(values, strings.TrimPrefix(entry, key+"="))
		}
	}
	return values
}
