package main

import (
	"net/http"
)

// todoCreateInput is what the user/UI/MCP sends to add a todo. Workspace
// is optional and free-form: when Claude calls add_todo it'll pass the
// pane cwd basename; the user-side input leaves it blank.
type todoCreateInput struct {
	Text      string `json:"text"`
	Workspace string `json:"workspace,omitempty"`
	Source    string `json:"source,omitempty"` // server clamps to "user" if not "claude"
	TaskID    string `json:"taskId,omitempty"`
}

type todoUpdateInput struct {
	Text   *string `json:"text,omitempty"`
	Done   *bool   `json:"done,omitempty"`
	TaskID *string `json:"taskId,omitempty"`
}

func (a *apiServer) listTodos(w http.ResponseWriter, _ *http.Request) {
	if a.todos == nil {
		writeJSON(w, 200, []*Todo{})
		return
	}
	writeJSON(w, 200, a.todos.List())
}

func (a *apiServer) createTodo(w http.ResponseWriter, r *http.Request) {
	var in todoCreateInput
	if !decodeJSON(w, r, &in, defaultJSONBodyBytes) {
		return
	}
	source := "user"
	if in.Source == "claude" {
		source = "claude"
	}
	text := in.Text
	originalText := ""
	if source == "user" {
		text = normalizeTodoDeterministic(in.Text)
		originalText = in.Text
	}
	td, err := a.todos.Add(text, in.Workspace, source, originalText, in.TaskID)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, 201, td)
	if source == "user" {
		go a.normalizeTodoAsync(td.ID, td.Text, true)
	}
}

func (a *apiServer) updateTodo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in todoUpdateInput
	if !decodeJSON(w, r, &in, defaultJSONBodyBytes) {
		return
	}
	td, err := a.todos.Update(id, in.Text, in.Done, in.TaskID)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, 200, td)
}

func (a *apiServer) deleteTodo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.todos.Delete(id); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(204)
}
