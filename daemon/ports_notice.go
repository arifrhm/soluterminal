package main

import (
	"path"
	"strings"
)

// Automatic toasts are reserved for identifiable preview servers. A TCP
// listener alone is not evidence of a web app. Custom/unknown servers remain
// available in the full port inventory without probing their private APIs.
func previewProcessLabel(p *Port) string {
	fields := strings.Fields(p.Cmdline)
	if len(fields) == 0 {
		fields = []string{p.Process}
	}
	base := func(s string) string {
		return strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(strings.Trim(s, "\"'"), "\\", "/")), ".exe"))
	}
	entry := base(fields[0])
	if entry == "node" || entry == "nodejs" || entry == "bun" || entry == "deno" {
		entry = ""
		for i := 1; i < len(fields); i++ {
			arg := fields[i]
			if arg == "--" {
				if i+1 < len(fields) {
					entry = base(fields[i+1])
				}
				break
			}
			// Do not treat a runtime option's value as the script being run.
			switch arg {
			case "--conditions", "-C", "--require", "-r", "--import", "--loader", "--experimental-loader":
				i++
				continue
			case "--eval", "-e", "--print", "-p":
				return ""
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			entry = base(arg)
			break
		}
	}
	switch entry {
	case "vite", "vite.js", "vite.mjs":
		return "Vite"
	case "next", "next-server":
		return "Next.js"
	case "webpack-dev-server", "webpack-dev-server.js":
		return "Webpack"
	case "http-server", "http-server.js", "live-server":
		return "Web preview"
	}
	// Next changes its process title to next-server (v<version>).
	if strings.HasPrefix(p.Cmdline, "next-server (v") {
		return "Next.js"
	}
	return ""
}
