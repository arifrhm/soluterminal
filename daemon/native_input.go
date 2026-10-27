package main

import (
	"errors"

	"net/http"
)

// paneNativeInput submits a semantic user turn to a Focus agent. Buffered
// delegated-work updates, when present, are prefixed as ambient context while
// the user's own text remains the durable/optimistic user row unchanged. The shared Pane
// path chooses Claude stream-json, Codex app-server turn RPC, or Codex's dedicated
// compact RPC and writes it through the FrameData transport. Protocol framing is
// never allowed into the terminal prompt recorder.
//
// NO PTY paste-burst Enter-split: that workaround exists only for TUIs that treat
// a body+\r in one frame as a paste burst; the agent reads newline-delimited JSON
// from stdin, so every adapter control envelope is newline-terminated and written
// atomically.
func (a *apiServer) paneNativeInput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := a.mgr.Get(id)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}

	_, isNative := p.NativeAdapterRef()
	if !isNative {
		http.Error(w, "pane is not native", 409)
		return
	}

	var req nativeInputReq
	if !decodeJSON(w, r, &req, defaultJSONBodyBytes) {
		return
	}
	if req.Text == "" {
		http.Error(w, "text is required", 400)
		return
	}

	result, err := p.submitNativeInputWithContext("", req.Text)
	if err != nil {
		if errors.Is(err, errNativeAgentNotReady) {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, 200, map[string]any{
		"bytes":      result.Bytes,
		"localMsgId": req.LocalMsgID,
	})
}
