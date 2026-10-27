package main

// Thin REST client for the Aria comms-gateway. The responder's REPLIES go
// out through a Claude session's aria-comms MCP tools (send_message) so the
// model owns the wording; this client is the daemon's own mouth — short
// deterministic notices (approval stalls, shutdown regrets) and schedule
// report delivery — where spawning a model run to post fixed text would be
// waste. Sends ride the gateway's normal grant/cap/journal pipeline and
// count against the key's daily send cap.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ariaSendMaxChars mirrors the gateway's MAX_TEXT_CHARS — longer sends 400.
const ariaSendMaxChars = 8000

type ariaGatewayChat struct {
	Source   string `json:"source"`
	ChatID   string `json:"chat_id"`
	ChatName string `json:"chat_name"`
	CanRead  bool   `json:"can_read"`
}

type ariaGatewayInfo struct {
	Agent   string
	Handle  string
	Mode    string // responder_mode; "" on pre-mode gateways
	Persona string
	Chats   []ariaGatewayChat
}

// ariaGatewayInfoCall POSTs {gateway}/v1/info as the key. Returns the HTTP
// status alongside the error so callers can tell a rejected key (401/403)
// from an unreachable gateway.
func ariaGatewayInfoCall(ctx context.Context, httpc *http.Client, gateway, key string) (*ariaGatewayInfo, int, error) {
	body, status, err := ariaGatewayPost(ctx, httpc, gateway+"/v1/info", key, "{}")
	if err != nil {
		return nil, status, err
	}
	var out struct {
		Agent         string            `json:"agent"`
		MentionHandle string            `json:"mention_handle"`
		ResponderMode string            `json:"responder_mode"`
		Persona       *string           `json:"persona"`
		Chats         []ariaGatewayChat `json:"chats"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, status, fmt.Errorf("parse info response: %w", err)
	}
	info := &ariaGatewayInfo{
		Agent:  out.Agent,
		Handle: out.MentionHandle,
		Mode:   out.ResponderMode,
		Chats:  out.Chats,
	}
	if out.Persona != nil {
		info.Persona = strings.TrimSpace(*out.Persona)
	}
	return info, status, nil
}

// ariaGatewaySend posts text into a granted chat as the agent via REST
// {gateway}/v1/send. Text over the gateway cap is truncated rather than
// bounced. Byte-bounded ≤ the cap is conservative: the gateway counts
// UTF-16 units, which never exceed the byte count.
func ariaGatewaySend(ctx context.Context, httpc *http.Client, gateway, key, source, chatID, text string) error {
	if len(text) > ariaSendMaxChars {
		text = truncateRunes(text, ariaSendMaxChars, "…")
	}
	payload, err := json.Marshal(map[string]string{
		"source": source, "chat_id": chatID, "text": text,
	})
	if err != nil {
		return err
	}
	_, _, err = ariaGatewayPost(ctx, httpc, gateway+"/v1/send", key, string(payload))
	return err
}

// truncateRunes cuts s so that the result (including suffix) is at most max
// bytes, never splitting a multibyte rune. Assumes len(suffix) < max.
func truncateRunes(s string, max int, suffix string) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(suffix)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

func ariaGatewayPost(ctx context.Context, httpc *http.Client, url, key, body string) ([]byte, int, error) {
	if strings.HasPrefix(url, "/") { // empty gateway: only the path is left
		return nil, 0, errAriaNoGateway
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("gateway HTTP %d: %s", resp.StatusCode, ariaTruncate(out, 200))
	}
	return out, resp.StatusCode, nil
}

// errAriaNoGateway: no ARIA comms gateway is configured (no agent gateway URL
// and no built-in default).
var errAriaNoGateway = errors.New("no ARIA comms gateway is configured")
