package main

import (
	"errors"
	"strings"
	"testing"
)

// These fixtures are adversarial on purpose: this module is the last line of
// defence before a secret reaches a crash report or the log ring. Every case
// asserts an exact result so a regression that *under*-redacts (leaks) or
// *over*-redacts (destroys debug info) fails loudly. The false-positive cases
// (stack frame and prose unchanged) carry as much weight as the true positives.

// TestRedactTruePositives covers the must-be-scrubbed cases.
func TestRedactTruePositives(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// Panic message embedding BOTH a home path and a real-looking
			// sk- token. Username dropped to "~", token replaced.
			name: "home_path_plus_sk_token",
			in:   `open /home/benjamin/.claude/.credentials.json: token sk-ant-api03-AbCdEf0123456789ZyXwVu failed`,
			want: `open ~/.claude/.credentials.json: token [REDACTED] failed`,
		},
		{
			// Bare 64-char hex machine token (256-bit) in a log line.
			name: "bare_hex_machine_token",
			in:   `machine token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`,
			want: `machine token: [REDACTED]`,
		},
		{
			// Authorization header with a long JWT-ish bearer token.
			name: "bearer_jwt",
			in:   `Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payloadpayloadpayload.sigsigsig`,
			want: `Authorization: Bearer [REDACTED]`,
		},
		{
			// /mnt/c/Users WSL home root, forward slashes.
			name: "mnt_c_users_path",
			in:   `/mnt/c/Users/benjamin/project/foo.go`,
			want: `~/project/foo.go`,
		},
		{
			// /Users macOS home root.
			name: "macos_users_path",
			in:   `/Users/benjamin/code/main.go:42`,
			want: `~/code/main.go:42`,
		},
		{
			// C:\Users Windows home root, back slashes preserved.
			name: "windows_backslash_path",
			in:   `C:\Users\benjamin\project\foo.go`,
			want: `~\project\foo.go`,
		},
		{
			// C:/Users Windows home root, forward slashes.
			name: "windows_forwardslash_path",
			in:   `C:/Users/benjamin/project/foo.go`,
			want: `~/project/foo.go`,
		},
		{
			// Bearer keyword is case-insensitive.
			name: "bearer_lowercase",
			in:   `auth=bearer AbCdEf0123456789GhIjKl`,
			want: `auth=Bearer [REDACTED]`,
		},
		{
			// Standalone OAuth-shaped opaque run (base64url, 40 chars).
			name: "opaque_oauth_run",
			in:   `refresh=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AB`,
			want: `refresh=[REDACTED]`,
		},
		{
			// AWS access-key id: 20 chars, BELOW the opaque floor, so only the
			// explicit prefix rule catches it.
			name: "aws_access_key_id",
			in:   `aws_key AKIAIOSFODNN7EXAMPLE end`,
			want: `aws_key [REDACTED] end`,
		},
		{
			// Short key=value secret (value well under 32 chars) — the KV rule
			// keeps the keyword and scrubs only the value.
			name: "kv_password_short_value",
			in:   `config PASSWORD=hunter2 loaded`,
			want: `config PASSWORD=[REDACTED] loaded`,
		},
		{
			// key: value form with a colon and spaces.
			name: "kv_secret_colon",
			in:   `api_key: abc123 next`,
			want: `api_key: [REDACTED] next`,
		},
		{
			// Underscore-prefixed env var — the keyword is a '_'-joined SUFFIX,
			// the form `env`/`.env` snapshots actually emit. A leading \b would
			// miss this (RE2 treats '_' as a word char).
			name: "kv_underscore_prefixed_env",
			in:   `DB_PASSWORD=hunter2`,
			want: `DB_PASSWORD=[REDACTED]`,
		},
		{
			// AWS secret access key in env form: value has '/' (breaks the opaque
			// run) and the keyword is deep in an underscore chain.
			name: "kv_aws_secret_access_key",
			in:   `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
			want: `AWS_SECRET_ACCESS_KEY=[REDACTED]`,
		},
		{
			name: "codex_env_access_token",
			in:   `CODEX_ACCESS_TOKEN=codex-access-token-short`,
			want: `CODEX_ACCESS_TOKEN=[REDACTED]`,
		},
		{
			name: "openai_api_key_env",
			in:   `OPENAI_API_KEY=sk-proj-AbCdEf0123456789GhIjKlMnOpQrStUvWxYz`,
			want: `OPENAI_API_KEY=[REDACTED]`,
		},
		{
			name: "codex_auth_json_tokens",
			in:   `{"access_token":"short-access","refresh_token":"short-refresh","id_token":"short-id","account_id":"acct-123"}`,
			want: `{"access_token":"[REDACTED]","refresh_token":"[REDACTED]","id_token":"[REDACTED]","account_id":"[REDACTED]"}`,
		},
		{
			// muse auth.json provider fields: api_key and expires_at ride the
			// same quoted-JSON rule, keys kept so the diagnostic still reads.
			name: "muse_auth_json_provider_secrets",
			in:   `{"mechanism":"oauth","api_key":"mk-abc","expires_at":"2026-09-01T00:00:00Z"}`,
			want: `{"mechanism":"oauth","api_key":"[REDACTED]","expires_at":"[REDACTED]"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSensitive(c.in)
			if got != c.want {
				t.Fatalf("redactSensitive(%q)\n  got  %q\n  want %q", c.in, got, c.want)
			}
			// Defence in depth: the original username must not survive any
			// path case, and no source token must appear in the output.
			if strings.Contains(got, "benjamin") && !strings.Contains(c.want, "benjamin") {
				t.Fatalf("username leaked: %q", got)
			}
		})
	}
}

// TestRedactFalsePositives covers the must-NOT-be-touched cases. Over-redaction
// destroys the debug value of a crash report, so these are mandatory.
func TestRedactFalsePositives(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string // want == in: unchanged
	}{
		{
			// A normal Go stack frame: function name with parens/dots and a
			// build-relative /app path must be left fully intact.
			name: "go_stack_frame",
			in:   "main.(*Updater).Check(...)\n\t/app/daemon/updater.go:282 +0x1a4",
		},
		{
			// Build-relative path on its own — no secret, needed for debugging.
			name: "app_relative_path",
			in:   `/app/daemon/updater_manifest.go`,
		},
		{
			// Ordinary prose.
			name: "ordinary_prose",
			in:   `the quick brown fox jumped over the lazy dog`,
		},
		{
			// A long-ish word that is still under the 32-char opaque threshold
			// and contains no token shape — must survive.
			name: "long_ordinary_word",
			in:   `internationalization happened here`,
		},
		{
			// Qualified Go identifier (dots) — the '.' separators keep any run
			// below 32 contiguous alphabet chars, so nothing should match.
			name: "qualified_identifier",
			in:   `github.com/example/soluterminal/daemon.(*Pane).Write`,
		},
		{
			// A secret KEYWORD in prose with no =/: delimiter must NOT trip the
			// key=value rule — otherwise ordinary logs get shredded.
			name: "kv_keyword_without_delimiter",
			in:   `the token was valid and the secret was safe`,
		},
		{
			// The keyword as a PREFIX of a longer word (no boundary before '=')
			// must NOT match — the trailing \b guards this even with the leading
			// \b removed.
			name: "kv_keyword_is_word_prefix",
			in:   `tokens=5 and passwords=2`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSensitive(c.in)
			if got != c.in {
				t.Fatalf("redactSensitive over-redacted\n  in   %q\n  got  %q", c.in, got)
			}
			if strings.Contains(got, "[REDACTED]") {
				t.Fatalf("unexpected redaction in %q -> %q", c.in, got)
			}
		})
	}
}

func TestRedactWorkerArtifact(t *testing.T) {
	t.Parallel()
	scrubbed := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "grok api key env",
			in:     "GROK_API_KEY=xai-GROKSECRET1234567890abcdef",
			want:   "GROK_API_KEY=[REDACTED]",
			secret: "xai-GROKSECRET1234567890abcdef",
		},
		{
			name:   "quoted grok api key env",
			in:     `export GROK_API_KEY="xai-GROKSECRET1234567890abcdef"`,
			want:   "export GROK_API_KEY=[REDACTED]",
			secret: "xai-GROKSECRET1234567890abcdef",
		},
		{
			name:   "quoted password",
			in:     `password: "hunter2"`,
			want:   "password: [REDACTED]",
			secret: "hunter2",
		},
		{
			name:   "comma separated api key value",
			in:     "api_key=abc,def,ghi",
			want:   "api_key=[REDACTED]",
			secret: "abc,def,ghi",
		},
		{
			name:   "deepseek api key env",
			in:     "DEEPSEEK_API_KEY=sk-DEEPSEEKSECRET1234567890abcdef",
			want:   "DEEPSEEK_API_KEY=[REDACTED]",
			secret: "sk-DEEPSEEKSECRET1234567890abcdef",
		},
		{
			name:   "openrouter api key env",
			in:     "OPENROUTER_API_KEY=sk-or-v1-OPENROUTERSECRET1234567890abcdef",
			want:   "OPENROUTER_API_KEY=[REDACTED]",
			secret: "sk-or-v1-OPENROUTERSECRET1234567890abcdef",
		},
		{
			name:   "bare openrouter api key",
			in:     "an OpenRouter key sk-or-v1-0123456789abcdef0123456789abcdef in prose",
			want:   "an OpenRouter key [REDACTED] in prose",
			secret: "sk-or-v1-0123456789abcdef0123456789abcdef",
		},
		{
			name:   "bearer token",
			in:     "Authorization: Bearer BEARERSECRET.eyJhbGciOiJIUzI1NiJ9.sig",
			want:   "Authorization: Bearer [REDACTED]",
			secret: "BEARERSECRET.eyJhbGciOiJIUzI1NiJ9.sig",
		},
		{
			name:   "json api key",
			in:     `{"provider":"grok","api_key":"JSONSECRETxai-0987654321"}`,
			want:   `{"provider":"grok","api_key":"[REDACTED]"}`,
			secret: "JSONSECRETxai-0987654321",
		},
		{
			name:   "bare github token",
			in:     "ghp_abcdefghijklmnopqrstuvwxyz0123",
			want:   "[REDACTED]",
			secret: "ghp_abcdefghijklmnopqrstuvwxyz0123",
		},
	}
	for _, tc := range scrubbed {
		t.Run(tc.name, func(t *testing.T) {
			got := redactWorkerArtifact(tc.in)
			if got != tc.want {
				t.Fatalf("redactWorkerArtifact(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q survived in %q", tc.secret, got)
			}
		})
	}

	preserved := []struct {
		name string
		in   string
	}{
		{
			name: "worker header path",
			in:   "# workflow worker · role=architect stage=spec · perm=plan · dir=/home/alice/wsl-projects/soluterminal · started=2026-08-22T10:00:00Z",
		},
		{
			name: "commit sha and source path",
			in:   "fixed in 3b1c2d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4 at /home/alice/wsl-projects/soluterminal/daemon/redact.go:61",
		},
		{
			name: "muse session id",
			in:   "runtime=muse session=11111111-2222-4333-8444-555555555555",
		},
		{
			name: "argv schema marker",
			in:   `# argv: ["/x/claude","-p","--permission-mode","plan","--json-schema","<json-schema 17 bytes>"]`,
		},
		{
			name: "json escaped argv schema marker",
			in:   `# argv: ["\u003cjson-schema 17 bytes\u003e"]`,
		},
		{
			name: "usage output",
			in:   `{"type":"result","usage":{"input_tokens":2,"output_tokens":3},"num_turns":1}`,
		},
		{
			name: "codex reasoning config",
			in:   "-c model_reasoning_effort=high",
		},
		{
			name: "invalid utf8",
			in:   string([]byte{'h', 'e', 'a', 'd', 0xff, ' ', 't', 'a', 'i', 'l'}),
		},
	}
	for _, tc := range preserved {
		t.Run(tc.name, func(t *testing.T) {
			got := redactWorkerArtifact(tc.in)
			if got != tc.in {
				t.Fatalf("redactWorkerArtifact changed diagnostic bytes\n  in   %q\n  got  %q", tc.in, got)
			}
			if strings.Contains(got, "[REDACTED]") {
				t.Fatalf("unexpected redaction in %q", got)
			}
		})
	}
}

// TestRedactStack confirms the []byte helper is equivalent to the string form
// and that a realistic multi-frame trace keeps its /app frames while scrubbing
// a leaked home path embedded in the message line.
func TestRedactStack(t *testing.T) {
	t.Parallel()
	trace := []byte("panic: open /home/benjamin/.claude/.credentials.json: no such file\n\n" +
		"goroutine 1 [running]:\n" +
		"main.(*Updater).Check(...)\n\t/app/daemon/updater.go:282 +0x1a4\n")
	got := redactStack(trace)
	if strings.Contains(got, "benjamin") {
		t.Fatalf("home username survived stack scrub: %q", got)
	}
	if !strings.Contains(got, "~/.claude/.credentials.json") {
		t.Fatalf("home path not rewritten to ~: %q", got)
	}
	if !strings.Contains(got, "main.(*Updater).Check(...)") {
		t.Fatalf("function frame was corrupted: %q", got)
	}
	if !strings.Contains(got, "/app/daemon/updater.go:282") {
		t.Fatalf("build-relative frame was corrupted: %q", got)
	}
	// Equivalence with the string form.
	if got != redactSensitive(string(trace)) {
		t.Fatalf("redactStack != redactSensitive(string(b))")
	}
}

// TestRedactPanicValue covers the three render paths: error, string, and an
// arbitrary value, each carrying a secret that must be scrubbed.
func TestRedactPanicValue(t *testing.T) {
	t.Parallel()
	if got := redactPanicValue(nil); got != "" {
		t.Fatalf("nil panic value: got %q, want \"\"", got)
	}

	errCase := errors.New("dial failed for sk-ant-api03-AbCdEf0123456789ZyXwVu")
	if got := redactPanicValue(errCase); strings.Contains(got, "sk-ant") {
		t.Fatalf("error panic leaked token: %q", got)
	} else if want := "dial failed for [REDACTED]"; got != want {
		t.Fatalf("error panic: got %q, want %q", got, want)
	}

	strCase := "  config at /home/benjamin/.config/solu.json  "
	if got := redactPanicValue(strCase); got != "config at ~/.config/solu.json" {
		t.Fatalf("string panic (trim+scrub): got %q", got)
	}

	// Arbitrary value via %v formatting.
	if got := redactPanicValue(42); got != "42" {
		t.Fatalf("int panic: got %q, want %q", got, "42")
	}
}

// TestRedactPEMBlock confirms a multi-line PEM private-key block is scrubbed in
// full (body newlines and all), while surrounding text survives. This matters
// for the feedback snapshot path, where a user might paste a key into the pane.
func TestRedactPEMBlock(t *testing.T) {
	t.Parallel()
	in := "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX\nGTpLZ3qXyz/abcDEF==\n-----END RSA PRIVATE KEY-----\nafter"
	got := redactSensitive(in)
	if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "MIIBOgIBAAJB") {
		t.Fatalf("PEM block not fully scrubbed: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("surrounding text destroyed: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
