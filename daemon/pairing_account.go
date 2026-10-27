package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Connecting with a signed-in account. A free account's paid features
// (cloud voice, smart approvals, phone access) need this computer connected
// to the cloud: the cloud only spends operator keys for an account that owns
// a connected machine. Before this, signing in left the computer unconnected
// and the mic answered with a `solud --claim` instruction.
//
// POST /api/cloud/pair/account mints a pairing code exactly like the pair
// panel, then accepts it on the user's behalf with the signed-in account's
// token (forwarded by the local web UI). The existing poll writes claim.json
// and the relay supervisor connects, so nothing else in pairing changes.
//
// After a sign-in the web asks the user once ("Connect this PC to your
// account?"): it checks first ({"check":true} answers available, connected
// or declined without pairing), then connects or records "Not now"
// ({"decline":true}). "Not now" and an explicit Disconnect share one marker,
// so the question is never asked again automatically; the Connect actions
// elsewhere send {"force":true}.

const (
	accountPairTimeout = 15 * time.Second
	// userDisconnectedMarker records an explicit "Disconnect" so an automatic
	// connect after sign-in never undoes it.
	userDisconnectedMarker = "cloud_disconnected_by_user"
	machineNameMax         = 100
)

type accountPairResult struct {
	Status  string `json:"status"` // available | connecting | connected | declined | machine_limit | signin | error
	Message string `json:"message,omitempty"`
}

func userDisconnected(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, userDisconnectedMarker))
	return err == nil
}

func setUserDisconnected(dataDir string, on bool) {
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, userDisconnectedMarker)
	if on {
		_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
		return
	}
	_ = os.Remove(path)
}

// defaultMachineName names the computer in the account's machine list.
func defaultMachineName() string {
	name, err := os.Hostname()
	name = strings.TrimSpace(name)
	if err != nil || name == "" {
		name = "My computer"
	}
	if len(name) > machineNameMax {
		name = name[:machineNameMax]
	}
	return name
}

func (a *apiServer) handleCloudPairAccount(w http.ResponseWriter, r *http.Request) {
	if pairRequestOverRelay(r) {
		http.Error(w, "pairing is local-only", http.StatusForbidden)
		return
	}
	if a.pairing == nil || a.pairing.relayURL == "" {
		writeJSON(w, http.StatusBadRequest, accountPairResult{Status: "error", Message: "This copy of SoluTerminal can't connect to the cloud."})
		return
	}
	token := strings.TrimSpace(r.Header.Get(entitlementsAccountHdr))
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, accountPairResult{Status: "signin", Message: "Sign in first."})
		return
	}
	var body struct {
		Force   bool `json:"force"`
		Check   bool `json:"check"`
		Decline bool `json:"decline"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if body.Decline {
		setUserDisconnected(a.dataDir, true)
		writeJSON(w, http.StatusOK, accountPairResult{Status: "declined"})
		return
	}

	if _, tok := a.cloudCreds(); tok != "" {
		writeJSON(w, http.StatusOK, accountPairResult{Status: "connected"})
		return
	}
	if c, err := LoadClaim(a.dataDir); err == nil && c != nil && strings.TrimSpace(c.Token) != "" {
		writeJSON(w, http.StatusOK, accountPairResult{Status: "connected"})
		return
	}
	if !body.Force && userDisconnected(a.dataDir) {
		writeJSON(w, http.StatusOK, accountPairResult{Status: "declined"})
		return
	}
	if body.Check {
		writeJSON(w, http.StatusOK, accountPairResult{Status: "available"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), accountPairTimeout)
	defer cancel()
	start, err := a.pairing.start(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, accountPairResult{Status: "error", Message: "Couldn't reach SoluTerminal's cloud. Check your connection and try again."})
		return
	}
	res, status := acceptClaimWithAccount(ctx, a.pairing.relayURL, token, start.Code, defaultMachineName())
	if res.Status != "connecting" {
		a.pairing.cancel()
		writeJSON(w, status, res)
		return
	}
	setUserDisconnected(a.dataDir, false)
	writeJSON(w, http.StatusOK, res)
}

// acceptClaimWithAccount accepts a pairing code with the account's token, as
// the website's "Connect a computer" page does.
func acceptClaimWithAccount(ctx context.Context, relayURL, accountToken, code, name string) (accountPairResult, int) {
	base, err := relayToHTTPBase(relayURL)
	if err != nil {
		return accountPairResult{Status: "error", Message: "This copy of SoluTerminal can't connect to the cloud."}, http.StatusBadRequest
	}
	payload, _ := json.Marshal(map[string]string{"code": code, "name": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/claim/accept", bytes.NewReader(payload))
	if err != nil {
		return accountPairResult{Status: "error", Message: "Couldn't start connecting."}, http.StatusInternalServerError
	}
	req.Header.Set("Authorization", "Bearer "+accountToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: accountPairTimeout}).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return accountPairResult{Status: "error", Message: "SoluTerminal's cloud took too long to answer. Try again."}, http.StatusGatewayTimeout
		}
		return accountPairResult{Status: "error", Message: "Couldn't reach SoluTerminal's cloud. Check your connection and try again."}, http.StatusBadGateway
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return accountPairResult{Status: "connecting"}, http.StatusOK
	}
	var cloudErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&cloudErr)
	switch {
	case cloudErr.Error == "machine_limit":
		return accountPairResult{Status: "machine_limit", Message: cloudErr.Message}, http.StatusConflict
	case resp.StatusCode == http.StatusUnauthorized:
		return accountPairResult{Status: "signin", Message: "Your sign-in expired. Sign in again."}, http.StatusUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		msg := cloudErr.Message
		if msg == "" {
			msg = "This account can't connect computers."
		}
		return accountPairResult{Status: "error", Message: msg}, http.StatusForbidden
	}
	msg := cloudErr.Message
	if msg == "" {
		msg = "Couldn't connect this computer. Try again."
	}
	return accountPairResult{Status: "error", Message: msg}, http.StatusBadGateway
}
