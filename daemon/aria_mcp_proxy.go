package main

// Loopback MCP proxy — the door that keeps the ariak_ key out of everything
// but aria_responder.json.
//
// Before: a pane's Claude (and every headless run) connected straight to
// {gateway}/mcp with `Authorization: Bearer <key>` baked into ~/.claude.json
// (user-scope registration) and into headless argv. Three costs: the key sat
// at rest in a second file, it leaked through /proc/<pid>/cmdline while a run
// was live, and rotating the key silently broke every already-running pane
// session (its config still carried the OLD secret).
//
// After: those clients point at a STABLE local URL — http://127.0.0.1:<port>/
// api/aria/mcp/<mcpName> — that carries NO secret. The daemon reverse-proxies
// each request to the agent's gateway /mcp, injecting the agent's CURRENT key
// server-side. So the key lives only in aria_responder.json (0600); rotation
// is transparent to running sessions (the URL never changes); and because
// every send now passes through the daemon, a pane reply actually reaching
// the gateway is finally observable (delivery confirmation, not typing-then-
// silence).
//
// Local-only by construction: the endpoint is loopback + not-relay gated, and
// the only legitimate caller is the same-machine Claude the daemon spawned or
// the user drives. Same-uid processes could already read the key from
// aria_responder.json, so this widens no trust boundary the daemon draws.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
)

// ariaMcpProxyURL is the stable local URL a pane/headless client points at for
// an agent's MCP server. Empty if the daemon's listen addr has no usable port
// (tests / misconfig) — callers then fall back to the direct-gateway path.
func ariaMcpProxyURL(listenAddr, name string) string {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return ""
	}
	return "http://127.0.0.1:" + port + "/api/aria/mcp/" + name
}

// handleAriaMcpProxy reverse-proxies POST /api/aria/mcp/{name} to the matching
// agent's gateway /mcp with its current key. Loopback-only.
func (a *apiServer) handleAriaMcpProxy(w http.ResponseWriter, r *http.Request) {
	// Streamable HTTP clients probe GET for an optional SSE stream. This
	// proxy only supports POST; 405 tells them to stop trying the stream.
	// A successful HTML response instead causes a reconnect every second.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.ariaResponder == nil {
		http.Error(w, "aria responder unavailable", http.StatusServiceUnavailable)
		return
	}
	// Dual gate: loopback AND not relay-forwarded (the cloud proxy stamps
	// X-Forwarded-Prefix and overwrites any client value, so remote traffic
	// can't forge "I'm local"). Nothing off-machine should reach an agent key.
	if !isLoopback(r) || requestViaRelay(r) {
		http.Error(w, "aria mcp proxy is local-only", http.StatusForbidden)
		return
	}
	agent, ok := a.ariaResponder.agentByMcpName(r.PathValue("name"))
	if !ok || !ariaAgentKeyRe.MatchString(agent.AgentKey) {
		http.Error(w, "no such aria agent", http.StatusNotFound)
		return
	}
	if agent.gateway() == "" {
		http.Error(w, errAriaNoGateway.Error(), http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(agent.gateway() + "/mcp")
	if err != nil {
		http.Error(w, "bad gateway url", http.StatusInternalServerError)
		return
	}
	// Observe send_message (best-effort) so a pane reply landing at the
	// gateway is visible to the owner. Read the body, then hand it back for
	// the proxy to forward — MCP requests are small JSON.
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		a.ariaResponder.observeMcpCall(agent.ID, body)
	}
	key := agent.AgentKey
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = target.Path
			req.URL.RawQuery = ""
			req.Host = target.Host
			// The daemon's own key is the only auth upstream; never forward
			// the client's daemon cookie/token.
			req.Header.Del("Cookie")
			req.Header.Set("Authorization", "Bearer "+key)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("aria-responder: mcp proxy to %s failed: %v", target.Host, err)
			http.Error(w, "gateway unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// agentByMcpName resolves an agent by its user-scope MCP server name.
func (r *AriaResponder) agentByMcpName(name string) (ariaAgentConfig, bool) {
	if name == "" {
		return ariaAgentConfig{}, false
	}
	for _, a := range r.store.get().Agents {
		if a.mcpName() == name {
			return a, true
		}
	}
	return ariaAgentConfig{}, false
}

// observeMcpCall records an agent's send_message tool call as delivery
// evidence on the owning runner's activity ring. Best-effort: a malformed or
// non-send body is silently ignored.
func (r *AriaResponder) observeMcpCall(agentID string, body []byte) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments struct {
				Source string `json:"source"`
				ChatID string `json:"chat_id"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return
	}
	if msg.Method != "tools/call" || msg.Params.Name != "send_message" {
		return
	}
	r.mu.Lock()
	run := r.runners[agentID]
	r.mu.Unlock()
	if run != nil {
		run.noteReply()
		run.recordActivity("sent", msg.Params.Arguments.Source+"/"+msg.Params.Arguments.ChatID, "reply reached the gateway")
	}
}

// migrateMcpToProxy re-points every configured agent's user-scope MCP
// registration at the keyless loopback proxy, once per install (guarded by a
// marker). Existing installs registered the key-bearing gateway URL directly;
// this moves them onto the proxy so the key stops living in ~/.claude.json.
// Idempotent and safe to skip when the listen addr is unknown (tests).
func (r *AriaResponder) migrateMcpToProxy() {
	if r.listenAddr == "" {
		return
	}
	marker := filepath.Join(r.dataDir, "aria_mcp_proxy.migrated")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	for _, a := range r.store.get().Agents {
		if !ariaAgentKeyRe.MatchString(a.AgentKey) {
			continue
		}
		if u := ariaMcpProxyURL(r.listenAddr, a.mcpName()); u != "" {
			registerClaudeMcp(u, a.mcpName())
		}
	}
	_ = os.WriteFile(marker, []byte("1"), 0o600)
}
