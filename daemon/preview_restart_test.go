package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func previewBootCloudClient(t *testing.T, deletes *[]string, failFirstDelete *bool) *http.Client {
	t.Helper()
	return &http.Client{Transport: previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		status := http.StatusNoContent
		body := ""
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/internal/previews":
			status = http.StatusCreated
			body = `{"previewId":"` + daemonPreviewTestID + `","url":"https://preview.example/preview/` + daemonPreviewTestID + `/","expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`
		case request.Method == http.MethodDelete:
			*deletes = append(*deletes, request.URL.Path)
			if failFirstDelete != nil && *failFirstDelete {
				*failFirstDelete = false
				status = http.StatusInternalServerError
			}
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
}

func previewRestartServe(api *apiServer, id, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/preview-serve/"+id+path, nil)
	request.SetPathValue("previewId", id)
	response := httptest.NewRecorder()
	api.servePreview(response, request)
	return response
}

func previewRestartWaitWake(t *testing.T, manager *previewManager, id string) {
	t.Helper()
	call, err := manager.triggerWake(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.done:
	case <-time.After(10 * time.Second):
		t.Fatal("preview wake did not finish")
	}
	if call.err != nil {
		t.Fatal(call.err)
	}
}

func TestPreviewRecipeBackedPreviewSurvivesDaemonRestart(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		t.Setenv("SOLU_PREVIEWS", "1")
		ctx := context.Background()
		dataDir := t.TempDir()
		cache := filepath.Join(dataDir, "preview-cache")
		source := t.TempDir()
		writePreviewFixture(t, source)
		digest, _, err := previewSealBundle(source, cache)
		if err != nil {
			t.Fatal(err)
		}
		var deletes []string
		creds := func() (string, string) { return "https://cloud.invalid", "token" }
		manager1, err := newPreviewManager(dataDir, "wsl", creds, cache)
		if err != nil {
			t.Fatal(err)
		}
		manager1.client = previewBootCloudClient(t, &deletes, nil)
		recipe := validPreviewRecipeForTest()
		recipe.AppDir, recipe.StaticDir, recipe.AcceptedDigest = ".", "dist", digest
		target, err := manager1.CreateFromRecipe(ctx, "wft_restart", t.TempDir(), recipe)
		if err != nil {
			t.Fatal(err)
		}
		if target.State != previewStateRunning {
			t.Fatalf("created target=%+v", target)
		}

		manager1.shutdownOwned()
		manager2, err := newPreviewManager(dataDir, "wsl", creds, cache)
		if err != nil {
			t.Fatal(err)
		}
		manager2.client = previewBootCloudClient(t, &deletes, nil)
		var bootStates []PreviewTarget
		manager2.onState = func(target PreviewTarget) { bootStates = append(bootStates, target) }
		reloaded, ok := manager2.Get(target.ID)
		if !ok || reloaded.State != previewStateIdle || reloaded.TargetPort != 0 {
			t.Fatalf("reloaded target=%+v ok=%v", reloaded, ok)
		}
		summary := manager2.reconcileBoot(ctx)
		if summary.Kept != 1 || summary.ExpiredAdopt != 0 || summary.ExpiredUnverifiable != 0 {
			t.Fatalf("boot summary=%+v", summary)
		}
		if _, ok := manager2.Get(target.ID); !ok {
			t.Fatal("recipe-backed preview expired at boot")
		}
		if len(deletes) != 0 {
			t.Fatalf("recipe-backed preview was revoked: %v", deletes)
		}
		if len(bootStates) != 1 || bootStates[0].State != previewStateIdle || bootStates[0].TaskID != "wft_restart" {
			t.Fatalf("boot states=%+v", bootStates)
		}
		manager2.onState = nil

		api := &apiServer{previews: manager2}
		response := previewRestartServe(api, target.ID, "/index.html")
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), "Preview waking up") {
			t.Fatalf("wake response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
		}
		previewRestartWaitWake(t, manager2, target.ID)
		index := previewRestartServe(api, target.ID, "/index.html")
		if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "accepted") {
			t.Fatalf("index status=%d body=%q", index.Code, index.Body.String())
		}
		asset := previewRestartServe(api, target.ID, "/assets/app.js")
		if asset.Code != http.StatusOK {
			t.Fatalf("asset status=%d body=%q", asset.Code, asset.Body.String())
		}
		served := t.TempDir()
		if err := os.MkdirAll(filepath.Join(served, "assets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(served, "index.html"), index.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(served, "assets", "app.js"), asset.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		servedDigest, err := previewTreeDigest(served)
		if err != nil || servedDigest != digest {
			t.Fatalf("served digest=%q want=%q err=%v", servedDigest, digest, err)
		}
		running, ok := manager2.Get(target.ID)
		if !ok || running.State != previewStateRunning || running.TargetPort == 0 {
			t.Fatalf("running target=%+v ok=%v", running, ok)
		}
		manager2.shutdownOwned()
	})

	t.Run("spawn", func(t *testing.T) {
		t.Setenv("SOLU_PREVIEWS", "1")
		ctx := context.Background()
		dataDir := t.TempDir()
		buildDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(buildDir, "accepted.txt"), []byte("accepted"), 0o600); err != nil {
			t.Fatal(err)
		}
		digest, err := previewTreeDigest(buildDir)
		if err != nil {
			t.Fatal(err)
		}
		manager1, err := newPreviewManager(dataDir, "wsl", nil)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		manager1.targets[daemonPreviewTestID] = &PreviewTarget{
			ID: daemonPreviewTestID, TaskID: "wft_spawn", BackendID: "wsl", Mode: previewModeSpawn,
			State: previewStateIdle, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
			CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AppDir: buildDir,
			BuildDir: buildDir, ServeArgv: previewHelperArgv(), Env: map[string]string{"PREVIEW_HELPER": "1"},
			HealthPath: "/", PublicBase: "relative", AcceptedDigest: digest,
		}
		if _, err := manager1.Relaunch(ctx, daemonPreviewTestID); err != nil {
			t.Fatal(err)
		}
		manager1.shutdownOwned()

		manager2, err := newPreviewManager(dataDir, "wsl", nil)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, ok := manager2.Get(daemonPreviewTestID)
		if !ok || reloaded.State != previewStateIdle || reloaded.TargetPort != 0 || reloaded.PID != 0 {
			t.Fatalf("reloaded target=%+v ok=%v", reloaded, ok)
		}
		summary := manager2.reconcileBoot(ctx)
		if summary.Kept != 1 || summary.ExpiredUnverifiable != 0 {
			t.Fatalf("boot summary=%+v", summary)
		}
		api := &apiServer{previews: manager2}
		response := previewRestartServe(api, daemonPreviewTestID, "/")
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), "Preview waking up") {
			t.Fatalf("wake response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
		}
		previewRestartWaitWake(t, manager2, daemonPreviewTestID)
		response = previewRestartServe(api, daemonPreviewTestID, "/")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "healthy preview") {
			t.Fatalf("healthy response status=%d body=%q", response.Code, response.Body.String())
		}
		manager2.shutdownOwned()
	})
}

func TestPreviewBootReconcileExpiresAdoptAndRevokesCloudRow(t *testing.T) {
	t.Setenv("SOLU_PREVIEWS", "1")
	ctx := context.Background()
	dataDir := t.TempDir()
	adopted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "still alive")
	}))
	defer adopted.Close()
	creds := func() (string, string) { return "https://cloud.invalid", "token" }
	manager1, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager1.targets[daemonPreviewTestID] = &PreviewTarget{
		ID: daemonPreviewTestID, TaskID: "wft_adopt", BackendID: "wsl", Mode: previewModeAdopt,
		TargetPort: serverPort(t, adopted.URL), State: previewStateRunning,
		URL:       "https://preview.example/preview/" + daemonPreviewTestID + "/",
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour),
	}
	manager1.mu.Lock()
	if err := manager1.saveLocked(); err != nil {
		manager1.mu.Unlock()
		t.Fatal(err)
	}
	manager1.mu.Unlock()

	manager2, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	var deletes []string
	manager2.client = previewBootCloudClient(t, &deletes, nil)
	var states []PreviewTarget
	manager2.onState = func(target PreviewTarget) { states = append(states, target) }
	if _, ok := manager2.Get(daemonPreviewTestID); !ok {
		t.Fatal("adopt target did not survive load for boot expiry")
	}
	summary := manager2.reconcileBoot(ctx)
	if summary.ExpiredAdopt != 1 || summary.Revoked != 0 || summary.PendingRevoke != 0 {
		t.Fatalf("boot summary=%+v", summary)
	}
	if _, ok := manager2.Get(daemonPreviewTestID); ok {
		t.Fatal("adopt target remained registered after boot")
	}
	wantDelete := "/api/internal/previews/" + daemonPreviewTestID
	if len(deletes) != 1 || deletes[0] != wantDelete {
		t.Fatalf("deletes=%v want=%q", deletes, wantDelete)
	}
	if len(states) != 1 || states[0].ID != daemonPreviewTestID || states[0].TaskID != "wft_adopt" || states[0].Mode != previewModeAdopt || states[0].State != previewStateExpired {
		t.Fatalf("states=%+v", states)
	}
	response := previewRestartServe(&apiServer{previews: manager2}, daemonPreviewTestID, "/")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "unknown preview") {
		t.Fatalf("serve response status=%d body=%q", response.Code, response.Body.String())
	}
	upstream, err := http.Get(adopted.URL)
	if err != nil {
		t.Fatalf("adopted process was stopped: %v", err)
	}
	defer upstream.Body.Close()
	upstreamBody, _ := io.ReadAll(upstream.Body)
	if upstream.StatusCode != http.StatusOK || !strings.Contains(string(upstreamBody), "still alive") {
		t.Fatalf("adopted response status=%d body=%q", upstream.StatusCode, upstreamBody)
	}
}

func TestPreviewBootReconcileRetriesRevokeUntilCloudAccepts(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	creds := func() (string, string) { return "https://cloud.invalid", "token" }
	manager1, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager1.targets[daemonPreviewTestID] = &PreviewTarget{
		ID: daemonPreviewTestID, TaskID: "wft_adopt", BackendID: "wsl", Mode: previewModeAdopt,
		TargetPort: 4321, State: previewStateRunning, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour),
	}
	manager1.mu.Lock()
	if err := manager1.saveLocked(); err != nil {
		manager1.mu.Unlock()
		t.Fatal(err)
	}
	manager1.mu.Unlock()

	manager2, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	var deletes []string
	failFirst := true
	manager2.client = previewBootCloudClient(t, &deletes, &failFirst)
	var states []PreviewTarget
	manager2.onState = func(target PreviewTarget) { states = append(states, target) }
	summary := manager2.reconcileBoot(ctx)
	if summary.ExpiredAdopt != 1 {
		t.Fatalf("boot summary=%+v", summary)
	}
	retained, ok := manager2.Get(daemonPreviewTestID)
	if !ok || retained.State != previewStateExpired || retained.ExpiresAt.After(time.Now()) {
		t.Fatalf("retained target=%+v ok=%v", retained, ok)
	}
	if _, _, _, ok := manager2.beginRequest(ctx, daemonPreviewTestID); ok {
		t.Fatal("expired revoke tombstone remained routable")
	}
	if len(states) != 1 || states[0].State != previewStateExpired {
		t.Fatalf("states=%+v", states)
	}
	manager2.sweep(ctx)
	if _, ok := manager2.Get(daemonPreviewTestID); ok {
		t.Fatal("successful retry did not remove tombstone")
	}
	if len(deletes) != 2 {
		t.Fatalf("delete attempts=%v", deletes)
	}
	if len(states) != 1 {
		t.Fatalf("retry emitted duplicate state event: %+v", states)
	}
}

func TestPreviewBootReconcileExpiresRecipeEntriesWhoseSealedDataIsGone(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *previewManager) string
	}{
		{
			name: "static",
			seed: func(t *testing.T, manager *previewManager) string {
				source := t.TempDir()
				writePreviewFixture(t, source)
				digest, bundle, err := previewSealBundle(source, manager.cacheRoot)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				manager.targets[daemonPreviewTestID] = &PreviewTarget{
					ID: daemonPreviewTestID, TaskID: "wft_static", BackendID: "wsl", Mode: previewModeStatic,
					State: previewStateRunning, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
					CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour),
					AcceptedDigest: digest, BundleDir: bundle, HealthPath: "/", SpaFallback: true,
				}
				return bundle
			},
		},
		{
			name: "spawn",
			seed: func(t *testing.T, manager *previewManager) string {
				buildDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(buildDir, "accepted.txt"), []byte("accepted"), 0o600); err != nil {
					t.Fatal(err)
				}
				digest, err := previewTreeDigest(buildDir)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				manager.targets[daemonPreviewTestID] = &PreviewTarget{
					ID: daemonPreviewTestID, TaskID: "wft_spawn", BackendID: "wsl", Mode: previewModeSpawn,
					State: previewStateIdle, URL: "https://preview.example/preview/" + daemonPreviewTestID + "/",
					CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), AppDir: buildDir,
					BuildDir: buildDir, ServeArgv: previewHelperArgv(), Env: map[string]string{"PREVIEW_HELPER": "1"},
					HealthPath: "/", PublicBase: "relative", AcceptedDigest: digest,
				}
				return buildDir
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			creds := func() (string, string) { return "https://cloud.invalid", "token" }
			manager1, err := newPreviewManager(dataDir, "wsl", creds)
			if err != nil {
				t.Fatal(err)
			}
			removedDir := test.seed(t, manager1)
			manager1.mu.Lock()
			if err := manager1.saveLocked(); err != nil {
				manager1.mu.Unlock()
				t.Fatal(err)
			}
			manager1.mu.Unlock()
			if err := os.RemoveAll(removedDir); err != nil {
				t.Fatal(err)
			}

			manager2, err := newPreviewManager(dataDir, "wsl", creds)
			if err != nil {
				t.Fatal(err)
			}
			var deletes []string
			manager2.client = previewBootCloudClient(t, &deletes, nil)
			var states []PreviewTarget
			manager2.onState = func(target PreviewTarget) { states = append(states, target) }
			if test.name == "static" {
				loaded, ok := manager2.Get(daemonPreviewTestID)
				if !ok || loaded.State != previewStateError {
					t.Fatalf("static load target=%+v ok=%v", loaded, ok)
				}
			}
			summary := manager2.reconcileBoot(ctx)
			if summary.ExpiredUnverifiable != 1 || len(deletes) != 1 {
				t.Fatalf("summary=%+v deletes=%v", summary, deletes)
			}
			if _, ok := manager2.Get(daemonPreviewTestID); ok {
				t.Fatal("unverifiable recipe target survived boot")
			}
			if len(states) != 1 || states[0].State != previewStateExpired || states[0].Mode != test.name {
				t.Fatalf("states=%+v", states)
			}
		})
	}
}

func TestPreviewLoadQueuesDroppedEntriesForCloudRevoke(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	manager1, err := newPreviewManager(dataDir, "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldID := "pv_11111111111111111111111111"
	tombstoneID := "pv_22222222222222222222222222"
	staleID := "pv_33333333333333333333333333"
	manager1.targets[oldID] = &PreviewTarget{
		ID: oldID, TaskID: "wft_old", BackendID: "wsl", Mode: previewModeAdopt, TargetPort: 3001,
		State: previewStateRunning, URL: "https://preview.example/preview/" + oldID + "/",
		CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	manager1.targets[tombstoneID] = &PreviewTarget{
		ID: tombstoneID, TaskID: "wft_tombstone", BackendID: "wsl", Mode: previewModeAdopt, TargetPort: 3002,
		State: previewStateExpired, URL: "https://preview.example/preview/" + tombstoneID + "/",
		CreatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}
	manager1.targets[staleID] = &PreviewTarget{
		ID: staleID, TaskID: "wft_stale", BackendID: "wsl", Mode: previewModeAdopt, TargetPort: 3003,
		State: previewStateRunning, URL: "https://preview.example/preview/" + staleID + "/",
		CreatedAt: now.Add(-48 * time.Hour), LastActivityAt: now.Add(-48 * time.Hour),
		ExpiresAt: now.Add(-previewRevokeRetryWindow - time.Minute),
	}
	manager1.targets["malformed"] = &PreviewTarget{
		ID: "malformed", TaskID: "wft_malformed", BackendID: "wsl", Mode: previewModeAdopt, TargetPort: 3004,
		State: previewStateRunning, URL: "https://preview.example/preview/malformed/",
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour),
	}
	manager1.mu.Lock()
	if err := manager1.saveLocked(); err != nil {
		manager1.mu.Unlock()
		t.Fatal(err)
	}
	manager1.mu.Unlock()

	creds := func() (string, string) { return "https://cloud.invalid", "token" }
	manager2, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldID, tombstoneID, staleID, "malformed"} {
		if _, ok := manager2.Get(id); ok {
			t.Fatalf("dropped registry entry %q remained live", id)
		}
	}
	if len(manager2.pendingRevokes) != 2 || manager2.pendingRevokes[oldID] == nil || manager2.pendingRevokes[tombstoneID] == nil {
		t.Fatalf("pending revokes=%+v", manager2.pendingRevokes)
	}
	var deleteAttempts []string
	manager2.client = &http.Client{Transport: previewRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodDelete {
			deleteAttempts = append(deleteAttempts, request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	var states []PreviewTarget
	manager2.onState = func(target PreviewTarget) { states = append(states, target) }
	summary := manager2.reconcileBoot(ctx)
	if summary.PendingRevoke != 2 || summary.Revoked != 0 || len(deleteAttempts) != 2 {
		t.Fatalf("summary=%+v deleteAttempts=%v", summary, deleteAttempts)
	}
	foundOld := false
	for _, state := range states {
		if state.TaskID == "wft_old" && state.State == previewStateExpired {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("old task was not announced expired: %+v", states)
	}
	raw, err := os.ReadFile(manager2.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), oldID) || !strings.Contains(string(raw), tombstoneID) || strings.Contains(string(raw), staleID) || strings.Contains(string(raw), "malformed") {
		t.Fatalf("persisted registry=%s", raw)
	}
	if strings.Count(string(raw), `"state": "expired"`) != 2 {
		t.Fatalf("pending tombstones were not persisted as expired: %s", raw)
	}

	manager3, err := newPreviewManager(dataDir, "wsl", creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager3.pendingRevokes) != 2 {
		t.Fatalf("second restart pending revokes=%+v", manager3.pendingRevokes)
	}
	var successfulDeletes []string
	manager2.client = previewBootCloudClient(t, &successfulDeletes, nil)
	manager2.sweep(ctx)
	if len(successfulDeletes) != 2 || len(manager2.pendingRevokes) != 0 {
		t.Fatalf("successful deletes=%v pending=%+v", successfulDeletes, manager2.pendingRevokes)
	}
	raw, err = os.ReadFile(manager2.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), oldID) || strings.Contains(string(raw), tombstoneID) {
		t.Fatalf("revoked tombstones remain persisted: %s", raw)
	}
}

func TestPreviewTriggerWakeRefusesExpiredTombstone(t *testing.T) {
	manager, err := newPreviewManager(t.TempDir(), "wsl", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tests := []struct {
		id        string
		state     string
		expiresAt time.Time
	}{
		{"pv_11111111111111111111111111", previewStateExpired, now.Add(time.Hour)},
		{"pv_22222222222222222222222222", previewStateIdle, now.Add(-time.Minute)},
	}
	for _, test := range tests {
		manager.targets[test.id] = &PreviewTarget{
			ID: test.id, Mode: previewModeStatic, State: test.state,
			URL: "https://preview.example/preview/" + test.id + "/", ExpiresAt: test.expiresAt,
		}
		if _, err := manager.Relaunch(context.Background(), test.id); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("Relaunch(%s) err=%v", test.id, err)
		}
		got, ok := manager.Get(test.id)
		if !ok || got.State != test.state {
			t.Fatalf("target after refused wake=%+v ok=%v", got, ok)
		}
		if manager.starting[test.id] != nil || manager.runtimes[test.id] != nil {
			t.Fatalf("expired target %s started", test.id)
		}
	}
}
