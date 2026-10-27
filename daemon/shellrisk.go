package main

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Parser-backed risk classification for Bash tool commands under "approve with
// caution". The substring needles (aria_native_auto.go) can't tell `git push`
// from `git log --grep push`, or `curl` from `echo curl` — every match is a
// whole-command substring. Here we parse the command with a real bash parser
// and classify each command the script would actually run: argv[0] (basename),
// its subcommand where that matters (git/npm/apt/...), destructive flag
// combinations (rm -r, reset --hard), and literal URLs in arguments.
//
// The classifier is a deterministic HOLD gate, exactly like the needles it
// replaces for Bash: a non-empty reason parks the request for a human; an
// empty reason still goes through the cautious LLM judge. So a false negative
// is judged, not auto-approved, and a false positive costs one manual click.
//
// Fail-closed contract: anything we cannot see through — a computed command
// name, `eval`, an unparseable script — is parked or (for parse errors)
// reported as unparsed so the caller falls back to the substring needles. An
// unparseable command must never end up MORE trusted than it was before.

const maxShellRecurseDepth = 4

// shellRiskReason classifies command. ok=true means the script parsed and
// reason is authoritative ("" = no deterministic risk). ok=false means the
// script did not parse — fall back to the substring needles.
func shellRiskReason(command string) (reason string, ok bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return "", false
	}
	return riskOfFile(f, 0), true
}

// riskOfFile walks every command (pipelines, lists, subshells, command
// substitutions, function bodies — Walk visits them all) and returns the first
// risk reason found.
func riskOfFile(f *syntax.File, depth int) string {
	var reason string
	syntax.Walk(f, func(n syntax.Node) bool {
		if reason != "" {
			return false
		}
		switch x := n.(type) {
		case *syntax.CallExpr:
			reason = riskOfCall(x, depth)
		case *syntax.Redirect:
			reason = riskOfRedirect(x)
		}
		return true
	})
	return reason
}

// riskOfRedirect flags bash's network-over-redirect trick (`> /dev/tcp/...`).
// Other sensitive redirect targets (/etc/, ~/.ssh, ...) are already covered by
// the global substring needles, which keep running alongside this classifier.
func riskOfRedirect(r *syntax.Redirect) string {
	if r.Word == nil {
		return ""
	}
	target, lit := literalWord(r.Word)
	if !lit {
		return ""
	}
	if strings.Contains(target, "/dev/tcp/") || strings.Contains(target, "/dev/udp/") {
		return "redirect opens a network socket (" + target + ")"
	}
	return ""
}

func riskOfCall(ce *syntax.CallExpr, depth int) string {
	if len(ce.Args) == 0 {
		return "" // bare assignment (VAR=value)
	}
	argv := make([]string, len(ce.Args))
	lit := make([]bool, len(ce.Args))
	for i, w := range ce.Args {
		argv[i], lit[i] = literalWord(w)
	}
	if !lit[0] {
		return "command name is computed at run time"
	}
	return classifyArgv(argv, lit, depth)
}

// literalWord flattens a word into its literal text. ok=false when any part is
// an expansion (parameter, command substitution, arithmetic, process subst) —
// i.e. the value is not knowable without running the script. Glob characters
// stay as text; that's fine for name/flag matching.
func literalWord(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	var rec func(parts []syntax.WordPart) bool
	rec = func(parts []syntax.WordPart) bool {
		for _, p := range parts {
			switch x := p.(type) {
			case *syntax.Lit:
				b.WriteString(x.Value)
			case *syntax.SglQuoted:
				b.WriteString(x.Value)
			case *syntax.DblQuoted:
				if !rec(x.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !rec(w.Parts) {
		return "", false
	}
	return b.String(), true
}

// Wrappers that run their argument as a command — unwrap and classify what
// they actually run. Value = flags that consume the NEXT argument.
var shellRunnerFlagArg = map[string]map[string]bool{
	"env":     {"-u": true, "-C": true, "-S": true},
	"xargs":   {"-n": true, "-P": true, "-L": true, "-s": true, "-d": true, "-a": true, "-E": true, "-I": true, "-i": true},
	"nice":    {"-n": true},
	"nohup":   {},
	"time":    {},
	"timeout": {"-k": true, "-s": true, "--kill-after": true, "--signal": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"setsid":  {},
	"command": {},
}

// Commands parked on sight, with the reason a human sees.
var shellAlwaysRisky = map[string]string{
	"sudo": "privilege escalation", "doas": "privilege escalation", "su": "privilege escalation",
	"curl": "network fetch", "wget": "network fetch",
	"nc": "raw network access", "ncat": "raw network access", "netcat": "raw network access",
	"socat": "raw network access", "telnet": "raw network access",
	"ssh": "remote execution", "scp": "remote file transfer", "sftp": "remote file transfer",
	"dd": "raw device write", "shred": "irrecoverable deletion",
	"fdisk": "disk partitioning", "parted": "disk partitioning", "wipefs": "disk wipe",
	"blkdiscard": "disk wipe", "mkswap": "disk format", "swapon": "swap control", "swapoff": "swap control",
	"chmod": "permission change", "chown": "ownership change", "chgrp": "ownership change",
	"setfacl": "permission change", "setcap": "capability change",
	"kill": "signals processes", "killall": "signals processes", "pkill": "signals processes",
	"shutdown": "power control", "reboot": "power control", "poweroff": "power control", "halt": "power control",
	"systemctl": "service control", "service": "service control", "launchctl": "service control",
	"crontab": "edits scheduled jobs", "at": "schedules commands", "batch": "schedules commands",
	"iptables": "firewall change", "nft": "firewall change", "ufw": "firewall change", "firewall-cmd": "firewall change",
	"useradd": "account change", "usermod": "account change", "userdel": "account change",
	"groupadd": "account change", "passwd": "account change",
	"mount": "filesystem mount", "umount": "filesystem mount",
	"modprobe": "kernel module change", "insmod": "kernel module change", "rmmod": "kernel module change",
	"eval": "defeats inspection",
	"exec": "replaces the shell / raw fd tricks",
	"pipx": "installs packages", "npx": "downloads and runs packages",
	"pacman": "package manager", "zypper": "package manager", "emerge": "package manager",
	"powershell": "Windows shell escape", "powershell.exe": "Windows shell escape",
	"pwsh": "Windows shell escape", "pwsh.exe": "Windows shell escape", "cmd.exe": "Windows shell escape",
}

// Commands whose risk lives in the subcommand (first non-flag argument).
var shellSubRisky = map[string]map[string]string{
	"npm":       {"install": "installs packages", "i": "installs packages", "ci": "installs packages", "add": "installs packages", "update": "installs packages", "upgrade": "installs packages", "uninstall": "removes packages", "publish": "publishes a package", "exec": "runs downloaded code", "link": "installs packages"},
	"pnpm":      {"install": "installs packages", "i": "installs packages", "add": "installs packages", "update": "installs packages", "remove": "removes packages", "dlx": "runs downloaded code", "link": "installs packages"},
	"yarn":      {"install": "installs packages", "add": "installs packages", "upgrade": "installs packages", "remove": "removes packages", "dlx": "runs downloaded code", "global": "installs packages"},
	"pip":       {"install": "installs packages", "uninstall": "removes packages", "download": "network fetch"},
	"pip3":      {"install": "installs packages", "uninstall": "removes packages", "download": "network fetch"},
	"gem":       {"install": "installs packages", "uninstall": "removes packages"},
	"cargo":     {"install": "installs packages", "publish": "publishes a package"},
	"go":        {"install": "installs packages", "get": "network fetch"},
	"apt":       {"install": "installs packages", "remove": "removes packages", "purge": "removes packages", "upgrade": "installs packages", "dist-upgrade": "installs packages", "autoremove": "removes packages"},
	"apt-get":   {"install": "installs packages", "remove": "removes packages", "purge": "removes packages", "upgrade": "installs packages", "dist-upgrade": "installs packages", "autoremove": "removes packages"},
	"dnf":       {"install": "installs packages", "remove": "removes packages", "upgrade": "installs packages"},
	"yum":       {"install": "installs packages", "remove": "removes packages", "update": "installs packages"},
	"apk":       {"add": "installs packages", "del": "removes packages"},
	"brew":      {"install": "installs packages", "uninstall": "removes packages", "upgrade": "installs packages"},
	"choco":     {"install": "installs packages", "uninstall": "removes packages"},
	"winget":    {"install": "installs packages", "uninstall": "removes packages"},
	"kubectl":   {"delete": "deletes cluster resources", "apply": "mutates cluster state", "drain": "drains a node"},
	"terraform": {"apply": "mutates infrastructure", "destroy": "destroys infrastructure"},
	"tofu":      {"apply": "mutates infrastructure", "destroy": "destroys infrastructure"},
	"pulumi":    {"up": "mutates infrastructure", "destroy": "destroys infrastructure"},
	"docker":    {"run": "runs a container", "exec": "runs in a container", "rm": "removes containers", "rmi": "removes images", "compose": "runs containers", "system": "prunes docker state", "volume": "mutates volumes", "network": "mutates networks", "push": "publishes an image"},
	"podman":    {"run": "runs a container", "exec": "runs in a container", "rm": "removes containers", "rmi": "removes images", "push": "publishes an image"},
}

// Commands where a literal URL argument is harmless text (display/search),
// exempted from the URL check below.
var shellURLTextSafe = map[string]bool{
	"echo": true, "printf": true, "grep": true, "rg": true, "ag": true,
	"cat": true, "head": true, "tail": true, "less": true, "sed": true,
	"awk": true, "cut": true, "sort": true, "uniq": true, "wc": true,
	"jq": true, "yq": true, "diff": true, "comm": true, "tr": true,
}

// Shells: `-c <script>` is re-parsed and classified recursively; a bare shell
// (script from stdin, e.g. `base64 -d | sh`) is opaque and parked. A shell
// running a script FILE is left to the LLM judge — same trust as `./script.sh`,
// which never enters this table.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
}

func classifyArgv(argv []string, lit []bool, depth int) string {
	// Unwrap runner chains (env/xargs/timeout/... → the real command), bounded
	// so a pathological `nice nice nice ...` can't loop.
	for hops := 0; hops < 8; hops++ {
		if len(argv) == 0 {
			return ""
		}
		if !lit[0] {
			return "command name is computed at run time"
		}
		name := commandBasename(argv[0])
		flagArgs, isRunner := shellRunnerFlagArg[name]
		if !isRunner {
			return classifyCommand(name, argv[1:], lit[1:], depth)
		}
		rest, restLit := argv[1:], lit[1:]
		// env: also skip VAR=value assignments.
		i := 0
		for i < len(rest) {
			a := rest[i]
			if name == "env" && restLit[i] && strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
				i++
				continue
			}
			if restLit[i] && strings.HasPrefix(a, "-") {
				if flagArgs[a] {
					i += 2
				} else {
					i++
				}
				continue
			}
			break
		}
		if name == "timeout" && i < len(rest) {
			i++ // the duration argument
		}
		if i >= len(rest) {
			return "" // runner with nothing to run
		}
		argv, lit = rest[i:], restLit[i:]
	}
	return "runner chain too deep to inspect"
}

func commandBasename(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func classifyCommand(name string, args []string, lit []bool, depth int) string {
	if why, hit := shellAlwaysRisky[name]; hit {
		return fmt.Sprintf("'%s' — %s", name, why)
	}
	if strings.HasPrefix(name, "mkfs") {
		return fmt.Sprintf("'%s' — disk format", name)
	}

	if subs, hit := shellSubRisky[name]; hit {
		sub, subLit := firstNonFlag(args, lit)
		if !subLit {
			return fmt.Sprintf("'%s' subcommand is computed at run time", name)
		}
		if why, bad := subs[sub]; bad {
			return fmt.Sprintf("'%s %s' — %s", name, sub, why)
		}
	}

	switch name {
	case "git":
		if r := gitRisk(args, lit); r != "" {
			return r
		}
	case "rm":
		if hasShortFlag(args, lit, 'r') || hasShortFlag(args, lit, 'R') || hasLongFlag(args, lit, "--recursive") {
			return "'rm' — recursive deletion"
		}
	case "find":
		if r := findRisk(args, lit, depth); r != "" {
			return r
		}
	case "truncate":
		if hasShortFlag(args, lit, 's') || hasLongFlag(args, lit, "--size") {
			return "'truncate' — resizes/destroys file contents"
		}
	case "sysctl":
		if hasShortFlag(args, lit, 'w') || anyLitContains(args, lit, "=") {
			return "'sysctl' — kernel parameter write"
		}
	case "rsync":
		for i, a := range args {
			if lit[i] && !strings.HasPrefix(a, "-") && (strings.Contains(a, ":") || strings.Contains(a, "@")) {
				return "'rsync' — remote transfer"
			}
		}
	}

	if shellInterpreters[name] {
		return interpreterRisk(name, args, lit, depth)
	}

	// Literal URLs in arguments (network intent) unless the command just
	// treats them as text.
	if !shellURLTextSafe[name] {
		for i, a := range args {
			if lit[i] && strings.Contains(a, "://") {
				return fmt.Sprintf("'%s' given a URL (%s)", name, truncateForReason(a))
			}
		}
	}
	return ""
}

func interpreterRisk(name string, args []string, lit []bool, depth int) string {
	for i, a := range args {
		if !lit[i] {
			return fmt.Sprintf("'%s' argument is computed at run time", name)
		}
		if a == "-c" {
			if i+1 >= len(args) {
				return ""
			}
			if depth+1 >= maxShellRecurseDepth {
				return "shell nesting too deep to inspect"
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(args[i+1]), "")
			if err != nil {
				return fmt.Sprintf("'%s -c' script does not parse", name)
			}
			return riskOfFile(f, depth+1)
		}
		if a == "-s" || a == "-" {
			return fmt.Sprintf("'%s' — runs a script from stdin", name)
		}
		if !strings.HasPrefix(a, "-") {
			return "" // script file — same trust as ./script.sh, judged by the LLM
		}
	}
	return fmt.Sprintf("'%s' — runs a script from stdin", name)
}

// gitRisk covers destructive/remote git forms beyond a flat subcommand match.
func gitRisk(args []string, lit []bool) string {
	sub, subLit := firstNonFlag(args, lit)
	if !subLit {
		return "'git' subcommand is computed at run time"
	}
	switch sub {
	case "push":
		return "'git push' — writes to a remote"
	case "clean":
		return "'git clean' — deletes untracked files"
	case "filter-branch", "filter-repo":
		return "'git " + sub + "' — history rewrite"
	case "reset":
		if hasLongFlag(args, lit, "--hard") || hasLongFlag(args, lit, "--merge") || hasLongFlag(args, lit, "--keep") {
			return "'git reset --hard' — discards working changes"
		}
	case "checkout":
		for i, a := range args {
			if lit[i] && (a == "--" || a == ".") {
				return "'git checkout --' — discards working changes"
			}
		}
	case "restore":
		// `git restore --staged <paths>` only unstages — harmless. Anything
		// touching the worktree discards edits.
		if !hasLongFlag(args, lit, "--staged") || hasLongFlag(args, lit, "--worktree") {
			return "'git restore' — discards working changes"
		}
	case "stash":
		next := afterToken(args, lit, "stash")
		if next == "drop" || next == "clear" {
			return "'git stash " + next + "' — deletes stashed changes"
		}
	}
	return ""
}

// findRisk parks `find ... -delete` and classifies `-exec <cmd> ...`.
func findRisk(args []string, lit []bool, depth int) string {
	for i := 0; i < len(args); i++ {
		if !lit[i] {
			continue
		}
		switch args[i] {
		case "-delete":
			return "'find -delete' — bulk deletion"
		case "-exec", "-execdir", "-ok", "-okdir":
			// Collect the command tokens up to the terminating ';' or '+'.
			j := i + 1
			var sub []string
			var subLit []bool
			for ; j < len(args); j++ {
				if lit[j] && (args[j] == ";" || args[j] == "+") {
					break
				}
				sub = append(sub, args[j])
				subLit = append(subLit, lit[j])
			}
			if len(sub) > 0 {
				if r := classifyArgv(sub, subLit, depth); r != "" {
					return "'find " + args[i] + "': " + r
				}
			}
			i = j
		}
	}
	return ""
}

func firstNonFlag(args []string, lit []bool) (string, bool) {
	for i, a := range args {
		if !lit[i] {
			return "", false
		}
		if !strings.HasPrefix(a, "-") {
			return a, true
		}
	}
	return "", true // flags only — no subcommand
}

// afterToken returns the literal token following the first occurrence of tok.
func afterToken(args []string, lit []bool, tok string) string {
	for i, a := range args {
		if lit[i] && a == tok && i+1 < len(args) && lit[i+1] {
			return args[i+1]
		}
	}
	// tok may be the resolved subcommand itself (not present in args when
	// firstNonFlag found it) — fall back to the token after the subcommand.
	sub, ok := firstNonFlag(args, lit)
	if ok && sub == tok {
		seen := false
		for i, a := range args {
			if !lit[i] {
				return ""
			}
			if seen && !strings.HasPrefix(a, "-") {
				return a
			}
			if a == tok {
				seen = true
			}
		}
	}
	return ""
}

func hasShortFlag(args []string, lit []bool, f byte) bool {
	for i, a := range args {
		if !lit[i] {
			continue
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], f) >= 0 {
			return true
		}
	}
	return false
}

func hasLongFlag(args []string, lit []bool, flag string) bool {
	for i, a := range args {
		if lit[i] && (a == flag || strings.HasPrefix(a, flag+"=")) {
			return true
		}
	}
	return false
}

func anyLitContains(args []string, lit []bool, s string) bool {
	for i, a := range args {
		if lit[i] && strings.Contains(a, s) {
			return true
		}
	}
	return false
}

func truncateForReason(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// ---- Prefix-allowlist support (arg-scoped "always allow" rules) ----

// shellArgvs returns the literal argv of every command the script runs.
// ok=false when anything is not fully visible: parse failure, a non-literal
// word anywhere, or a redirect that touches a file (fd dups and pipes are
// fine). Prefix allowlist rules must never match a script they can't fully
// see — `git status $(evil)` or `git status > ~/.bashrc` must not ride a
// "git status" rule.
func shellArgvs(command string) ([][]string, bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	var out [][]string
	sound := true
	syntax.Walk(f, func(n syntax.Node) bool {
		if !sound {
			return false
		}
		switch x := n.(type) {
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				return true
			}
			argv := make([]string, len(x.Args))
			for i, w := range x.Args {
				v, lit := literalWord(w)
				if !lit {
					sound = false
					return false
				}
				argv[i] = v
			}
			out = append(out, argv)
		case *syntax.Redirect:
			// Allow fd duplication (2>&1); any file-touching redirect defeats
			// a prefix rule's guarantee.
			if x.Op != syntax.DplOut && x.Op != syntax.DplIn {
				sound = false
				return false
			}
		}
		return true
	})
	if !sound || len(out) == 0 {
		return nil, false
	}
	return out, true
}
