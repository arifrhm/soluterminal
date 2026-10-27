package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeTodoDeterministic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "whitespace and punctuation", in: "  fix   the bug!! ", want: "fix the bug"},
		{name: "question preserved", in: "  investigate this? ", want: "investigate this?"},
		{name: "interior punctuation preserved", in: "review auth, then docs", want: "review auth, then docs"},
		{name: "already clean", in: "Review the auth flow", want: "Review the auth flow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTodoDeterministic(tt.in); got != tt.want {
				t.Fatalf("normalizeTodoDeterministic(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func newTodoTestStore(t *testing.T) (*TodoStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewTodoStore(dir, newUIBus())
	if err != nil {
		t.Fatal(err)
	}
	return store, dir
}

func findTodo(t *testing.T, store *TodoStore, id string) *Todo {
	t.Helper()
	for _, td := range store.List() {
		if td.ID == id {
			return td
		}
	}
	t.Fatalf("todo %q not found", id)
	return nil
}

func TestTodoStoreOriginalTextRoundTrip(t *testing.T) {
	t.Parallel()
	store, dir := newTodoTestStore(t)
	differing, err := store.Add("fix the bug", "", "user", "  fix   the bug!! ", "")
	if err != nil {
		t.Fatal(err)
	}
	identical, err := store.Add("already clean", "", "user", "already clean", "")
	if err != nil {
		t.Fatal(err)
	}
	if differing.OriginalText != "  fix   the bug!! " {
		t.Fatalf("originalText = %q", differing.OriginalText)
	}
	if identical.OriginalText != "" {
		t.Fatalf("identical originalText was stored: %q", identical.OriginalText)
	}

	reloaded, err := NewTodoStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := findTodo(t, reloaded, differing.ID).OriginalText; got != differing.OriginalText {
		t.Fatalf("reloaded originalText = %q, want %q", got, differing.OriginalText)
	}
}

func TestTodoStoreTaskLinkPersistsWithoutOtherMutation(t *testing.T) {
	t.Parallel()
	store, dir := newTodoTestStore(t)
	td, err := store.Add("Review the auth flow", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	taskID := "wft_x"
	updated, err := store.Update(td.ID, nil, nil, &taskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.TaskID != taskID || updated.Text != td.Text || updated.Done != td.Done {
		t.Fatalf("task-link update changed unrelated fields: %+v", updated)
	}

	reloaded, err := NewTodoStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := findTodo(t, reloaded, td.ID).TaskID; got != taskID {
		t.Fatalf("reloaded taskId = %q, want %q", got, taskID)
	}
}

func TestTodoStoreTextUpdateClearsOnlyDuplicateFlag(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	td, err := store.Add("Review auth", "", "user", "  Review auth. ", "")
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := store.ApplyNormalization(td.ID, td.Text, td.Text, "", "todo_other"); err != nil || !applied {
		t.Fatalf("ApplyNormalization() = %v, %v", applied, err)
	}
	reworded := "Review authentication"
	updated, err := store.Update(td.ID, &reworded, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DuplicateOf != "" {
		t.Fatalf("duplicateOf survived text change: %+v", updated)
	}
	if updated.OriginalText != "  Review auth. " {
		t.Fatalf("originalText was not preserved: %+v", updated)
	}
}

func TestApplyNormalizationCompareAndSetAndNeverMerges(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	duplicate, err := store.Add("Audit the auth flow", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.Add("Review auth", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := store.ApplyNormalization(target.ID, "Review auth", "Review authentication", "repo", duplicate.ID)
	if err != nil || !applied {
		t.Fatalf("ApplyNormalization() = %v, %v", applied, err)
	}
	got := findTodo(t, store, target.ID)
	if got.Text != "Review authentication" || got.Workspace != "repo" || got.DuplicateOf != duplicate.ID || got.OriginalText != "Review auth" {
		t.Fatalf("normalized todo = %+v", got)
	}
	if len(store.List()) != 2 || findTodo(t, store, duplicate.ID).Done || got.Done {
		t.Fatalf("duplicate annotation merged, deleted, or completed a todo: %+v", store.List())
	}

	withWorkspace, err := store.Add("Document auth", "existing", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err = store.ApplyNormalization(withWorkspace.ID, withWorkspace.Text, withWorkspace.Text, "suggested", "")
	if err != nil || !applied {
		t.Fatalf("workspace normalization = %v, %v", applied, err)
	}
	if got := findTodo(t, store, withWorkspace.ID).Workspace; got != "existing" {
		t.Fatalf("existing workspace overwritten with %q", got)
	}
}

func TestApplyNormalizationSkipsDoneOrEditedTodo(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	doneTodo, err := store.Add("Ship release", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := store.Update(doneTodo.ID, nil, &done, nil); err != nil {
		t.Fatal(err)
	}
	if applied, err := store.ApplyNormalization(doneTodo.ID, doneTodo.Text, "Publish release", "repo", "todo_other"); err != nil || applied {
		t.Fatalf("done normalization = %v, %v; want false, nil", applied, err)
	}
	if got := findTodo(t, store, doneTodo.ID); got.Text != doneTodo.Text || got.Workspace != "" || got.DuplicateOf != "" {
		t.Fatalf("done todo mutated: %+v", got)
	}

	editedTodo, err := store.Add("Review docs", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	expectText := editedTodo.Text
	edited := "Review API docs"
	if _, err := store.Update(editedTodo.ID, &edited, nil, nil); err != nil {
		t.Fatal(err)
	}
	if applied, err := store.ApplyNormalization(editedTodo.ID, expectText, "Rewrite docs", "repo", doneTodo.ID); err != nil || applied {
		t.Fatalf("stale normalization = %v, %v; want false, nil", applied, err)
	}
	if got := findTodo(t, store, editedTodo.ID); got.Text != edited || got.Workspace != "" || got.DuplicateOf != "" {
		t.Fatalf("edited todo mutated: %+v", got)
	}
}

func newTodoTestAPI(t *testing.T) (*apiServer, http.Handler) {
	t.Helper()
	store, _ := newTodoTestStore(t)
	a := &apiServer{todos: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/todos", a.listTodos)
	mux.HandleFunc("POST /api/todos", a.createTodo)
	mux.HandleFunc("PATCH /api/todos/{id}", a.updateTodo)
	return a, mux
}

func todoAPIRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTodoAPICreateNormalizesWithoutAria(t *testing.T) {
	t.Parallel()
	a, handler := newTodoTestAPI(t)
	if a.aria != nil {
		t.Fatal("test requires absent ARIA model plumbing")
	}
	rec := todoAPIRequest(handler, http.MethodPost, "/api/todos", `{"text":"  fix   the bug!! "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var td Todo
	if err := json.Unmarshal(rec.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}
	if td.Text != "fix the bug" || td.OriginalText != "  fix   the bug!! " {
		t.Fatalf("created todo = %+v", td)
	}
}

func TestTodoAPICreateKeepsClaudeTextOutOfNormalization(t *testing.T) {
	a, handler := newTodoTestAPI(t)
	a.aria = &Aria{cloudBase: "https://todo.test", cloudTok: "token"}
	requested := make(chan struct{}, 1)
	oldClient := ariaClient.Load()
	ariaClient.Store(&http.Client{Transport: todoRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested <- struct{}{}
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unexpected")), Request: req}, nil
	})})
	t.Cleanup(func() { ariaClient.Store(oldClient) })

	rec := todoAPIRequest(handler, http.MethodPost, "/api/todos", `{"text":"  raw!!  ","source":"claude"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var td Todo
	if err := json.Unmarshal(rec.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}
	if td.Text != "raw!!" || td.OriginalText != "" || td.Source != "claude" {
		t.Fatalf("Claude todo was normalized: %+v", td)
	}
	select {
	case <-requested:
		t.Fatal("Claude-created todo scheduled model normalization")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTodoAPICreateRejectsTextEmptyAfterCleanup(t *testing.T) {
	t.Parallel()
	_, handler := newTodoTestAPI(t)
	rec := todoAPIRequest(handler, http.MethodPost, "/api/todos", `{"text":"  ...!!; "}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "text is required") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestTodoAPICreateCleanTextOmitsOriginalText(t *testing.T) {
	t.Parallel()
	_, handler := newTodoTestAPI(t)
	rec := todoAPIRequest(handler, http.MethodPost, "/api/todos", `{"text":"Review auth","taskId":"wft_created"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var td Todo
	if err := json.Unmarshal(rec.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}
	if td.Text != "Review auth" || td.OriginalText != "" || td.TaskID != "wft_created" {
		t.Fatalf("clean todo = %+v", td)
	}
}

func TestTodoAPIPatchTaskLinkAndDoneCompatibility(t *testing.T) {
	t.Parallel()
	_, handler := newTodoTestAPI(t)
	created := todoAPIRequest(handler, http.MethodPost, "/api/todos", `{"text":"Review auth"}`)
	var td Todo
	if err := json.Unmarshal(created.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}

	linked := todoAPIRequest(handler, http.MethodPatch, "/api/todos/"+td.ID, `{"taskId":"wft_x"}`)
	if linked.Code != http.StatusOK {
		t.Fatalf("task link status = %d, body = %s", linked.Code, linked.Body.String())
	}
	if err := json.Unmarshal(linked.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}
	if td.TaskID != "wft_x" || td.Done {
		t.Fatalf("linked todo = %+v", td)
	}

	completed := todoAPIRequest(handler, http.MethodPatch, "/api/todos/"+td.ID, `{"done":true}`)
	if completed.Code != http.StatusOK {
		t.Fatalf("done-only status = %d, body = %s", completed.Code, completed.Body.String())
	}
	if err := json.Unmarshal(completed.Body.Bytes(), &td); err != nil {
		t.Fatal(err)
	}
	if !td.Done || td.TaskID != "wft_x" {
		t.Fatalf("done-only patch broke linkage compatibility: %+v", td)
	}
}

type todoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f todoRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func todoNormalizationSSE(reply string) string {
	delta, _ := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": reply},
	})
	return strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"cheap-test-model","usage":{"input_tokens":7}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		"data: " + string(delta),
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
}

func setTodoNormalizationResponse(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	oldClient := ariaClient.Load()
	ariaClient.Store(&http.Client{Transport: todoRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})})
	t.Cleanup(func() { ariaClient.Store(oldClient) })
	return &requests
}

func assertTodoNormalizationUnchanged(t *testing.T, store *TodoStore, target *Todo) {
	t.Helper()
	got := findTodo(t, store, target.ID)
	if got.Text != target.Text || got.Workspace != target.Workspace || got.DuplicateOf != target.DuplicateOf || got.OriginalText != target.OriginalText {
		t.Fatalf("todo mutated after rejected normalization: got %+v, want %+v", got, target)
	}
}

func TestNormalizeTodoAsyncAppliesValidatedSuggestions(t *testing.T) {
	store, _ := newTodoTestStore(t)
	duplicate, err := store.Add("Audit auth", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.Add("Review auth", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}

	usageSeen := make(chan struct{}, 1)
	oldClient := ariaClient.Load()
	ariaClient.Store(&http.Client{Transport: todoRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		status := http.StatusOK
		switch req.URL.Path {
		case "/api/aria/complete":
			body = todoNormalizationSSE(`{"text":"Audit the auth flow.","workspace":"repo","duplicateOf":"` + duplicate.ID + `"}`)
		case "/api/aria/usage":
			usageSeen <- struct{}{}
			status = http.StatusNoContent
		default:
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})})
	t.Cleanup(func() { ariaClient.Store(oldClient) })

	a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}
	a.normalizeTodoAsync(target.ID, target.Text, true)
	got := findTodo(t, store, target.ID)
	if got.Text != "Audit the auth flow" || got.Workspace != "repo" || got.DuplicateOf != duplicate.ID || got.OriginalText != "Review auth" {
		t.Fatalf("async normalization = %+v", got)
	}
	select {
	case <-usageSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("todo normalization usage was not reported")
	}
}

func TestNormalizeTodoAsyncAcceptsWorkspaceFromCompletedTodo(t *testing.T) {
	store, _ := newTodoTestStore(t)
	completed, err := store.Add("Archive release notes", "release", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := store.Update(completed.ID, nil, &done, nil); err != nil {
		t.Fatal(err)
	}
	target, err := store.Add("Review checklist", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	setTodoNormalizationResponse(t, http.StatusOK, todoNormalizationSSE(`{"text":"Review the checklist","workspace":"release","duplicateOf":""}`))
	a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}

	a.normalizeTodoAsync(target.ID, target.Text, true)

	got := findTodo(t, store, target.ID)
	if got.Text != "Review the checklist" || got.Workspace != "release" {
		t.Fatalf("completed-todo workspace was not reused: %+v", got)
	}
}

func TestNormalizeTodoAsyncRejectsInvalidModelOutput(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{name: "malformed json", reply: `not json`},
		{name: "empty cleaned text", reply: `{"text":"...!!;","workspace":"","duplicateOf":""}`},
		{name: "too long text", reply: `{"text":"` + strings.Repeat("x", 121) + `","workspace":"","duplicateOf":""}`},
		{name: "unknown workspace", reply: `{"text":"Audit authentication","workspace":"unknown","duplicateOf":""}`},
		{name: "unknown duplicate", reply: `{"text":"Audit authentication","workspace":"","duplicateOf":"todo_missing"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := newTodoTestStore(t)
			if _, err := store.Add("Audit auth", "repo", "user", "", ""); err != nil {
				t.Fatal(err)
			}
			target, err := store.Add("Review auth", "", "user", "", "")
			if err != nil {
				t.Fatal(err)
			}
			setTodoNormalizationResponse(t, http.StatusOK, todoNormalizationSSE(tt.reply))
			a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}

			a.normalizeTodoAsync(target.ID, target.Text, true)

			assertTodoNormalizationUnchanged(t, store, target)
		})
	}
}

func TestNormalizeTodoAsyncFailurePathsLeaveTodoUntouched(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "non-200 response", status: http.StatusServiceUnavailable, body: "unavailable"},
		{name: "incomplete stream", status: http.StatusOK, body: `data: {"type":"message_start","message":{"model":"cheap-test-model"}}` + "\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := newTodoTestStore(t)
			target, err := store.Add("Review auth", "", "user", "", "")
			if err != nil {
				t.Fatal(err)
			}
			setTodoNormalizationResponse(t, tt.status, tt.body)
			a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}

			a.normalizeTodoAsync(target.ID, target.Text, true)

			assertTodoNormalizationUnchanged(t, store, target)
		})
	}
}

func TestNormalizeTodoAsyncSkipsMissingAriaOrCloudCredentials(t *testing.T) {
	store, _ := newTodoTestStore(t)
	target, err := store.Add("Review auth", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	requests := setTodoNormalizationResponse(t, http.StatusOK, todoNormalizationSSE(`{"text":"Audit auth","workspace":"","duplicateOf":""}`))

	(&apiServer{todos: store}).normalizeTodoAsync(target.ID, target.Text, true)
	(&apiServer{todos: store, aria: &Aria{}}).normalizeTodoAsync(target.ID, target.Text, true)

	if got := requests.Load(); got != 0 {
		t.Fatalf("missing model configuration made %d cloud requests", got)
	}
	assertTodoNormalizationUnchanged(t, store, target)
}

func TestMCPTaskLinkOnlyUpdateAndListMetadata(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	td, err := store.Add("Review auth", "", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	a := &apiServer{todos: store}
	if text, isErr := mcpResultText(t, a.mcpToolUpdateTodo(map[string]any{"id": td.ID, "taskId": "wft_x"})); isErr {
		t.Fatalf("taskId-only update failed: %s", text)
	}
	text, isErr := mcpResultText(t, a.mcpToolListTodos(nil))
	if isErr {
		t.Fatalf("list_todos failed: %s", text)
	}
	var listed []struct {
		CreatedAt int64  `json:"createdAt"`
		TaskID    string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].CreatedAt == 0 || listed[0].TaskID != "wft_x" {
		t.Fatalf("list_todos metadata = %+v", listed)
	}
}

func TestTodoStoreAddAcceptsTaskID(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	td, err := store.Add("Review auth", "repo", "claude", "", "  wft_x  ")
	if err != nil {
		t.Fatal(err)
	}
	if td.TaskID != "wft_x" || findTodo(t, store, td.ID).TaskID != "wft_x" {
		t.Fatalf("taskId was not stored at creation: %+v", td)
	}
}

func TestMCPAddTodoReturnsCreatedTodoAndTaskLink(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	a := &apiServer{todos: store}
	text, isErr := mcpResultText(t, a.mcpToolAddTodo(map[string]any{
		"text": "Review the auth flow", "workspace": " repo ", "taskId": "wft_x",
	}))
	if isErr {
		t.Fatalf("add_todo failed: %s", text)
	}
	var result struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Workspace string `json:"workspace"`
		TaskID    string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(text, "\n", 2)[0]), &result); err != nil {
		t.Fatalf("first result line is not strict JSON: %v (%q)", err, text)
	}
	if result.ID == "" || result.Text != "Review the auth flow" || result.Workspace != "repo" || result.TaskID != "wft_x" {
		t.Fatalf("add_todo result = %+v", result)
	}
	if got := findTodo(t, store, result.ID); got.TaskID != "wft_x" {
		t.Fatalf("stored todo = %+v", got)
	}
}

func TestMCPAddTodoNearDuplicateAdvisory(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	original, err := store.Add("Review authentication flow", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	a := &apiServer{todos: store}

	text, isErr := mcpResultText(t, a.mcpToolAddTodo(map[string]any{"text": "Review the authentication flow"}))
	if isErr || !strings.Contains(text, "Possible duplicate of "+original.ID) {
		t.Fatalf("near-duplicate result = %q, error=%v", text, isErr)
	}
	var result struct {
		ID          string `json:"id"`
		DuplicateOf string `json:"duplicateOf"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(text, "\n", 2)[0]), &result); err != nil {
		t.Fatal(err)
	}
	if result.DuplicateOf != original.ID || findTodo(t, store, result.ID).DuplicateOf != original.ID {
		t.Fatalf("duplicate advisory was not persisted: result=%+v todo=%+v", result, findTodo(t, store, result.ID))
	}

	dissimilar, dissimilarErr := mcpResultText(t, a.mcpToolAddTodo(map[string]any{"text": "Buy oat milk"}))
	if dissimilarErr || strings.Contains(dissimilar, "Possible duplicate") {
		t.Fatalf("dissimilar add result = %q, error=%v", dissimilar, dissimilarErr)
	}

	doneCandidate, err := store.Add("Publish release checklist", "", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := store.Update(doneCandidate.ID, nil, &done, nil); err != nil {
		t.Fatal(err)
	}
	againstDone, againstDoneErr := mcpResultText(t, a.mcpToolAddTodo(map[string]any{"text": "Publish the release checklist"}))
	if againstDoneErr || strings.Contains(againstDone, "Possible duplicate of "+doneCandidate.ID) {
		t.Fatalf("done todo produced advisory: %q, error=%v", againstDone, againstDoneErr)
	}
}

func TestMCPAddTodoAsyncNormalizationPreservesDeterministicDuplicate(t *testing.T) {
	store, _ := newTodoTestStore(t)
	original, err := store.Add("Review authentication flow", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	usageSeen := make(chan struct{}, 1)
	oldClient := ariaClient.Load()
	ariaClient.Store(&http.Client{Transport: todoRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := ""
		switch req.URL.Path {
		case "/api/aria/complete":
			body = todoNormalizationSSE(`{"text":"Review the authentication flow","workspace":"repo","duplicateOf":""}`)
		case "/api/aria/usage":
			usageSeen <- struct{}{}
			status = http.StatusNoContent
		default:
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})})
	t.Cleanup(func() { ariaClient.Store(oldClient) })

	a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}
	text, isErr := mcpResultText(t, a.mcpToolAddTodo(map[string]any{"text": "Review the authentication flow"}))
	if isErr {
		t.Fatalf("add_todo failed: %s", text)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(text, "\n", 2)[0]), &result); err != nil {
		t.Fatal(err)
	}
	select {
	case <-usageSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("async MCP todo normalization did not finish")
	}
	if got := findTodo(t, store, result.ID).DuplicateOf; got != original.ID {
		t.Fatalf("async normalization replaced deterministic duplicateOf: got %q, want %q", got, original.ID)
	}
}

func TestMCPAddTodoReturnsCreatedIDWhenDuplicatePersistenceFails(t *testing.T) {
	store, _ := newTodoTestStore(t)
	if _, err := store.Add("Review authentication flow", "", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	realWrite := store.writeFile
	writes := 0
	store.writeFile = func(path string, f todoFile) error {
		writes++
		if writes == 2 {
			return errors.New("injected advisory write failure")
		}
		return realWrite(path, f)
	}

	text, isErr := mcpResultText(t, (&apiServer{todos: store}).mcpToolAddTodo(map[string]any{
		"text": "Review the authentication flow",
	}))
	if isErr {
		t.Fatalf("created todo id was hidden by advisory failure: %s", text)
	}
	var result struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(text, "\n", 2)[0]), &result); err != nil {
		t.Fatalf("first result line is not recoverable JSON: %v (%q)", err, text)
	}
	if result.ID == "" || result.Text != "Review the authentication flow" || findTodo(t, store, result.ID).ID != result.ID {
		t.Fatalf("created todo was not recoverable: result=%+v todos=%+v", result, store.List())
	}
	if !strings.Contains(text, "duplicate advisory could not be saved") {
		t.Fatalf("result omitted advisory persistence warning: %q", text)
	}
}

func TestFindNearDuplicateTodo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		text      string
		candidate *Todo
		excludeID string
		want      bool
	}{
		{name: "exact after normalization", text: "Review auth!!!", candidate: &Todo{ID: "a", Text: "  review   auth. "}, want: true},
		{name: "high overlap", text: "Review the authentication release flow", candidate: &Todo{ID: "a", Text: "Review authentication release flow"}, want: true},
		{name: "containment", text: "Audit auth before release", candidate: &Todo{ID: "a", Text: "audit auth"}, want: true},
		{name: "below threshold", text: "Review docs", candidate: &Todo{ID: "a", Text: "Review authentication"}, want: false},
		{name: "excludes self", text: "Review auth", candidate: &Todo{ID: "a", Text: "Review auth"}, excludeID: "a", want: false},
		{name: "excludes done", text: "Review auth", candidate: &Todo{ID: "a", Text: "Review auth", Done: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findNearDuplicateTodo(tt.text, []*Todo{tt.candidate}, tt.excludeID)
			if (got != nil) != tt.want {
				t.Fatalf("findNearDuplicateTodo() = %+v, want match=%v", got, tt.want)
			}
		})
	}
}

func TestTodoWorkspaceCanonicalizedOnWrite(t *testing.T) {
	t.Parallel()
	store, _ := newTodoTestStore(t)
	if _, err := store.Add("First", "Soluterminal", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	td, err := store.Add("Second", "  soluterminal ", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if td.Workspace != "Soluterminal" {
		t.Fatalf("workspace = %q, want canonical spelling", td.Workspace)
	}

	a := &apiServer{todos: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/todos", a.createTodo)
	rec := todoAPIRequest(mux, http.MethodPost, "/api/todos", `{"text":"Third","workspace":" SOLUTERMINAL "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created Todo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Workspace != "Soluterminal" {
		t.Fatalf("HTTP workspace = %q, want canonical spelling", created.Workspace)
	}
}

func TestTodoWorkspaceMigrationIsLosslessAndIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UnixMilli()
	fixture := []*Todo{
		{ID: "one", Text: "First", CreatedAt: now - 3000, Workspace: "soluterminal", Source: "user", TaskID: "wft_one"},
		{ID: "two", Text: "Second", CreatedAt: now - 2000, Workspace: " soluterminal ", Source: "claude", OriginalText: "Second."},
		{ID: "three", Text: "Third", Done: true, CreatedAt: now - 1000, DoneAt: now - int64(5*24*time.Hour/time.Millisecond), Workspace: "Soluterminal", Source: "user", TaskID: "wft_three"},
	}
	path := filepath.Join(dir, "todos.json")
	if err := writeTodoFile(path, todoFile{Todos: fixture, Order: []string{"three", "two", "one"}}); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]Todo)
	for _, td := range fixture {
		want[td.ID] = *td
	}

	store, err := NewTodoStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, td := range store.List() {
		before := want[td.ID]
		if td.Workspace != "soluterminal" {
			t.Fatalf("workspace for %s = %q", td.ID, td.Workspace)
		}
		if td.ID != before.ID || td.Text != before.Text || td.Done != before.Done || td.CreatedAt != before.CreatedAt || td.DoneAt != before.DoneAt || td.TaskID != before.TaskID {
			t.Fatalf("migration changed non-workspace data: before=%+v after=%+v", before, td)
		}
	}
	firstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := time.Unix(123456789, 0)
	if err := os.Chtimes(path, marker, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTodoStore(dir, nil); err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) || !info.ModTime().Equal(marker) {
		t.Fatalf("second load rewrote an already-migrated file (mtime=%v)", info.ModTime())
	}
}

func TestTodoWorkspaceMigrationTieBreakIsDeterministic(t *testing.T) {
	t.Parallel()
	for i := 0; i < 128; i++ {
		lower := &Todo{ID: "lower", Workspace: "solu", CreatedAt: 0}
		upper := &Todo{ID: "upper", Workspace: "Solu", CreatedAt: 0}
		store := &TodoStore{items: map[string]*Todo{lower.ID: lower, upper.ID: upper}}
		if !store.migrateWorkspaceTagsUnlocked() {
			t.Fatal("tied variants were not migrated")
		}
		if lower.Workspace != "Solu" || upper.Workspace != "Solu" {
			t.Fatalf("iteration %d chose nondeterministic spelling: lower=%q upper=%q", i, lower.Workspace, upper.Workspace)
		}
	}
}

func TestTodoLoadWriteFailuresDoNotPreventStartup(t *testing.T) {
	t.Run("workspace migration save", func(t *testing.T) {
		dir := t.TempDir()
		fixture := []*Todo{
			{ID: "lower", Text: "First", Workspace: "solu", CreatedAt: 1},
			{ID: "upper", Text: "Second", Workspace: "Solu", CreatedAt: 2},
		}
		if err := writeTodoFile(filepath.Join(dir, "todos.json"), todoFile{Todos: fixture, Order: []string{"upper", "lower"}}); err != nil {
			t.Fatal(err)
		}
		store, err := newTodoStore(dir, nil, func(string, todoFile) error {
			return errors.New("injected migration save failure")
		})
		if err != nil {
			t.Fatalf("migration persistence failure prevented startup: %v", err)
		}
		if len(store.List()) != 2 || store.List()[0].Workspace != "Solu" || store.List()[1].Workspace != "Solu" {
			t.Fatalf("in-memory migration was not retained: %+v", store.List())
		}
	})

	t.Run("archive active-file save", func(t *testing.T) {
		dir := t.TempDir()
		old := &Todo{ID: "old", Text: "Old done", Done: true, CreatedAt: 1, DoneAt: time.Now().Add(-91 * 24 * time.Hour).UnixMilli()}
		if err := writeTodoFile(filepath.Join(dir, "todos.json"), todoFile{Todos: []*Todo{old}, Order: []string{old.ID}}); err != nil {
			t.Fatal(err)
		}
		store, err := newTodoStore(dir, nil, func(path string, f todoFile) error {
			if strings.HasSuffix(path, "todos-archive.json") {
				return writeTodoFile(path, f)
			}
			return errors.New("injected active-file save failure")
		})
		if err != nil {
			t.Fatalf("archive persistence failure prevented startup: %v", err)
		}
		if got := store.List(); len(got) != 1 || got[0].ID != old.ID {
			t.Fatalf("failed sweep was not restored in memory: %+v", got)
		}
		archive, err := store.LoadArchive()
		if err != nil || len(archive) != 1 || archive[0].ID != old.ID {
			t.Fatalf("archive copy was not retained safely: archive=%+v err=%v", archive, err)
		}
	})
}

func TestTodoArchivalMovesOldDoneItemsWithoutDuplication(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	old := &Todo{ID: "old", Text: "Old done", Done: true, CreatedAt: now.Add(-100 * 24 * time.Hour).UnixMilli(), DoneAt: now.Add(-91 * 24 * time.Hour).UnixMilli(), Source: "user"}
	recent := &Todo{ID: "recent", Text: "Recent done", Done: true, CreatedAt: now.Add(-6 * 24 * time.Hour).UnixMilli(), DoneAt: now.Add(-5 * 24 * time.Hour).UnixMilli(), Source: "user"}
	open := &Todo{ID: "open", Text: "Still open", CreatedAt: now.UnixMilli(), Source: "user"}
	if err := writeTodoFile(filepath.Join(dir, "todos.json"), todoFile{Todos: []*Todo{open, recent, old}, Order: []string{"open", "recent", "old"}}); err != nil {
		t.Fatal(err)
	}

	store, err := NewTodoStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 2 || findTodo(t, store, recent.ID).ID != recent.ID {
		t.Fatalf("active todos after archive = %+v", store.List())
	}
	archive, err := store.LoadArchive()
	if err != nil || len(archive) != 1 || archive[0].ID != old.ID {
		t.Fatalf("archive = %+v, error=%v", archive, err)
	}
	if _, err := readTodoFile(filepath.Join(dir, "todos.json")); err != nil {
		t.Fatalf("active JSON: %v", err)
	}
	if _, err := readTodoFile(filepath.Join(dir, "todos-archive.json")); err != nil {
		t.Fatalf("archive JSON: %v", err)
	}
	reloaded, err := NewTodoStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	archive, err = reloaded.LoadArchive()
	if err != nil || len(archive) != 1 || archive[0].ID != old.ID {
		t.Fatalf("archive after second load = %+v, error=%v", archive, err)
	}
}

func TestMCPListTodosFiltersAndArchive(t *testing.T) {
	t.Parallel()
	store, dir := newTodoTestStore(t)
	if _, err := store.Add("Review Auth Flow", "Soluterminal", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("Write website copy", "website", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	cItem, err := store.Add("Ship auth docs", "Soluterminal", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	a := &apiServer{todos: store}
	done := true
	if _, err := store.Update(cItem.ID, nil, &done, nil); err != nil {
		t.Fatal(err)
	}
	archived := &Todo{ID: "archived", Text: "Archived auth audit", Done: true, CreatedAt: 1, DoneAt: 2, Workspace: "Soluterminal", Source: "user"}
	if err := writeTodoFile(filepath.Join(dir, "todos-archive.json"), todoFile{Todos: []*Todo{archived}, Order: []string{archived.ID}}); err != nil {
		t.Fatal(err)
	}

	list := func(args map[string]any) []map[string]any {
		t.Helper()
		text, isErr := mcpResultText(t, a.mcpToolListTodos(args))
		if isErr {
			t.Fatalf("list_todos failed: %s", text)
		}
		var result []map[string]any
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	nilText, nilErr := mcpResultText(t, a.mcpToolListTodos(nil))
	emptyText, emptyErr := mcpResultText(t, a.mcpToolListTodos(map[string]any{}))
	if nilErr || emptyErr || nilText != emptyText || strings.Contains(nilText, "archived") {
		t.Fatalf("default output changed: nil=%q empty=%q", nilText, emptyText)
	}
	if got := list(map[string]any{"done": true}); len(got) != 1 || got[0]["id"] != cItem.ID {
		t.Fatalf("done filter = %+v", got)
	}
	if got := list(map[string]any{"workspace": " soluterminal "}); len(got) != 2 {
		t.Fatalf("workspace filter = %+v", got)
	}
	if got := list(map[string]any{"query": "AUTH"}); len(got) != 2 {
		t.Fatalf("query filter = %+v", got)
	}
	if got := list(map[string]any{"limit": float64(1)}); len(got) != 1 || got[0]["id"] != cItem.ID {
		t.Fatalf("limit filter = %+v", got)
	}
	if got := list(map[string]any{"limit": 0.5}); len(got) != 3 {
		t.Fatalf("fractional sub-one limit should be ignored: %+v", got)
	}
	if got := list(map[string]any{"includeArchived": true, "query": "archived"}); len(got) != 1 || got[0]["id"] != archived.ID || got[0]["archived"] != true {
		t.Fatalf("archive listing = %+v", got)
	}
}

func TestNormalizeTodoAsyncPreservesTextWhenRewriteDisabled(t *testing.T) {
	store, _ := newTodoTestStore(t)
	duplicate, err := store.Add("Audit auth", "repo", "user", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.Add("Agent-authored wording", "", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	setTodoNormalizationResponse(t, http.StatusOK, todoNormalizationSSE(`{"text":"Rewritten wording","workspace":"repo","duplicateOf":"`+duplicate.ID+`"}`))
	a := &apiServer{todos: store, aria: &Aria{cloudBase: "https://todo.test", cloudTok: "token"}}

	a.normalizeTodoAsync(target.ID, target.Text, false)

	got := findTodo(t, store, target.ID)
	if got.Text != target.Text || got.OriginalText != "" || got.Workspace != "repo" || got.DuplicateOf != duplicate.ID {
		t.Fatalf("rewrite-disabled normalization = %+v", got)
	}
}
