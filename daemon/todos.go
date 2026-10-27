package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// todoArchiveAfter is the age after which completed todos move to
// todos-archive.json. Archived todos are excluded from List (and therefore
// /api/todos, the sidebar done count, and list_todos by default), but remain
// available through LoadArchive and list_todos with includeArchived.
const todoArchiveAfter = 90 * 24 * time.Hour

// Todo is a single backlog item — captured by the user (left-panel input)
// or by Claude (via the add_todo MCP tool). Global, not per-workspace:
// the inbox is meant to be a zero-friction capture surface, and forcing
// the user to pick a destination at write time defeats the point. The
// optional Workspace tag powers filtered views without having to consolidate
// fragmented files.
type Todo struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	Done         bool   `json:"done"`
	CreatedAt    int64  `json:"createdAt"`           // unix millis
	DoneAt       int64  `json:"doneAt,omitempty"`    // unix millis, 0 if not done
	Workspace    string `json:"workspace,omitempty"` // free-form tag (cwd basename when added by Claude)
	Source       string `json:"source"`              // "user" | "claude"
	TaskID       string `json:"taskId,omitempty"`
	OriginalText string `json:"originalText,omitempty"`
	DuplicateOf  string `json:"duplicateOf,omitempty"`
}

// TodoStore is the JSON-backed persistence + in-memory mirror.
// Mirrors the ScheduleStore shape (sync.Mutex around a map + ordered ID
// slice, atomic tmp+rename writes) so we don't introduce a new pattern
// for one feature.
type TodoStore struct {
	mu          sync.Mutex
	items       map[string]*Todo
	order       []string // newest-first
	path        string
	archivePath string
	writeFile   func(string, todoFile) error
	uiBus       *uiBus
}

func NewTodoStore(dataDir string, bus *uiBus) (*TodoStore, error) {
	return newTodoStore(dataDir, bus, writeTodoFile)
}

func newTodoStore(dataDir string, bus *uiBus, writeFile func(string, todoFile) error) (*TodoStore, error) {
	t := &TodoStore{
		items:       map[string]*Todo{},
		path:        filepath.Join(dataDir, "todos.json"),
		archivePath: filepath.Join(dataDir, "todos-archive.json"),
		writeFile:   writeFile,
		uiBus:       bus,
	}
	if err := t.load(); err != nil {
		return nil, err
	}
	return t, nil
}

type todoFile struct {
	Todos []*Todo  `json:"todos"`
	Order []string `json:"order"`
}

func (t *TodoStore) load() error {
	data, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var f todoFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	for _, td := range f.Todos {
		if td == nil || td.ID == "" {
			continue
		}
		t.items[td.ID] = td
	}
	if len(f.Order) > 0 {
		t.order = f.Order
	} else {
		// Back-fill order — newest first by createdAt for files written
		// before order tracking existed.
		for _, td := range f.Todos {
			if td != nil && td.ID != "" {
				t.order = append(t.order, td.ID)
			}
		}
		sort.SliceStable(t.order, func(i, j int) bool {
			return t.items[t.order[i]].CreatedAt > t.items[t.order[j]].CreatedAt
		})
	}
	if t.migrateWorkspaceTagsUnlocked() {
		if err := t.saveUnlocked(); err != nil {
			log.Printf("todos: workspace tag migration could not be persisted: %v", err)
		}
	}
	t.archiveOldDoneUnlocked(time.Now())
	return nil
}

func (t *TodoStore) saveUnlocked() error {
	list := make([]*Todo, 0, len(t.order))
	for _, id := range t.order {
		if td, ok := t.items[id]; ok {
			list = append(list, td)
		}
	}
	return t.writeFile(t.path, todoFile{Todos: list, Order: t.order})
}

func writeTodoFile(path string, f todoFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".solu-tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// migrateWorkspaceTagsUnlocked folds case/whitespace-only tag variants into a
// single spelling without changing any other todo field. The most-used
// spelling wins; ties go to the spelling on the most recently created todo.
func (t *TodoStore) migrateWorkspaceTagsUnlocked() bool {
	type variantStat struct {
		spelling string
		count    int
		latest   int64
	}
	type tagGroup struct {
		variants []*variantStat
		byName   map[string]*variantStat
	}
	groups := make(map[string]*tagGroup)
	for _, td := range t.items {
		spelling := strings.TrimSpace(td.Workspace)
		if spelling == "" {
			continue
		}
		key := strings.ToLower(spelling)
		group := groups[key]
		if group == nil {
			group = &tagGroup{byName: make(map[string]*variantStat)}
			groups[key] = group
		}
		stat := group.byName[spelling]
		if stat == nil {
			stat = &variantStat{spelling: spelling}
			group.byName[spelling] = stat
			group.variants = append(group.variants, stat)
		}
		stat.count++
		if td.CreatedAt > stat.latest {
			stat.latest = td.CreatedAt
		}
	}
	canonical := make(map[string]string, len(groups))
	for key, group := range groups {
		winner := group.variants[0]
		for _, candidate := range group.variants[1:] {
			if candidate.count > winner.count ||
				(candidate.count == winner.count && candidate.latest > winner.latest) ||
				(candidate.count == winner.count && candidate.latest == winner.latest && candidate.spelling < winner.spelling) {
				winner = candidate
			}
		}
		canonical[key] = winner.spelling
	}

	changed := false
	for _, td := range t.items {
		trimmed := strings.TrimSpace(td.Workspace)
		if trimmed == "" {
			if td.Workspace != "" {
				td.Workspace = ""
				changed = true
			}
			continue
		}
		if workspace := canonical[strings.ToLower(trimmed)]; td.Workspace != workspace {
			td.Workspace = workspace
			changed = true
		}
	}
	return changed
}

func (t *TodoStore) archiveOldDoneUnlocked(now time.Time) {
	cutoff := now.Add(-todoArchiveAfter).UnixMilli()
	swept := make([]*Todo, 0)
	for _, id := range t.order {
		td, ok := t.items[id]
		if ok && td.Done && td.DoneAt > 0 && td.DoneAt < cutoff {
			swept = append(swept, td)
		}
	}
	if len(swept) == 0 {
		return
	}

	archive, err := readTodoFile(t.archivePath)
	if err != nil {
		log.Printf("todos: archive sweep skipped: read archive: %v", err)
		return
	}
	archivedIDs := make(map[string]bool, len(archive.Todos)+len(swept))
	for _, td := range archive.Todos {
		if td != nil && td.ID != "" {
			archivedIDs[td.ID] = true
		}
	}
	for _, td := range swept {
		if !archivedIDs[td.ID] {
			cp := *td
			archive.Todos = append(archive.Todos, &cp)
			archivedIDs[td.ID] = true
		}
	}
	archive.Order = archive.Order[:0]
	for _, td := range archive.Todos {
		if td != nil && td.ID != "" {
			archive.Order = append(archive.Order, td.ID)
		}
	}
	if err := t.writeFile(t.archivePath, archive); err != nil {
		log.Printf("todos: archive sweep skipped: write archive: %v", err)
		return
	}

	previousOrder := append([]string(nil), t.order...)
	for _, td := range swept {
		delete(t.items, td.ID)
	}
	activeOrder := t.order[:0]
	for _, id := range t.order {
		if _, ok := t.items[id]; ok {
			activeOrder = append(activeOrder, id)
		}
	}
	t.order = activeOrder
	if err := t.saveUnlocked(); err != nil {
		for _, td := range swept {
			t.items[td.ID] = td
		}
		t.order = previousOrder
		log.Printf("todos: archive sweep could not persist active backlog; keeping swept items active: %v", err)
	}
}

func readTodoFile(path string) (todoFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return todoFile{}, nil
	}
	if err != nil {
		return todoFile{}, err
	}
	var f todoFile
	if err := json.Unmarshal(data, &f); err != nil {
		return todoFile{}, err
	}
	return f, nil
}

// LoadArchive loads completed todos that have aged out of the active backlog.
func (t *TodoStore) LoadArchive() ([]*Todo, error) {
	f, err := readTodoFile(t.archivePath)
	if err != nil {
		return nil, err
	}
	out := make([]*Todo, 0, len(f.Todos))
	for _, td := range f.Todos {
		if td == nil || td.ID == "" {
			continue
		}
		cp := *td
		out = append(out, &cp)
	}
	return out, nil
}

// List returns todos in display order (newest first).
func (t *TodoStore) List() []*Todo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Todo, 0, len(t.order))
	for _, id := range t.order {
		if td, ok := t.items[id]; ok {
			cp := *td
			out = append(out, &cp)
		}
	}
	return out
}

// Add inserts a new todo at the top of the list. Empty text is rejected
// here so callers (HTTP, MCP) don't each have to validate.
func (t *TodoStore) Add(text, workspace, source, originalText, taskID string) (*Todo, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text is required")
	}
	if source == "" {
		source = "user"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	td := &Todo{
		ID:           generateTodoID(),
		Text:         text,
		CreatedAt:    time.Now().UnixMilli(),
		Workspace:    t.canonicalWorkspaceLocked(workspace),
		Source:       source,
		TaskID:       strings.TrimSpace(taskID),
		OriginalText: originalText,
	}
	if td.OriginalText == td.Text {
		td.OriginalText = ""
	}
	t.items[td.ID] = td
	t.order = append([]string{td.ID}, t.order...) // newest first
	if err := t.saveUnlocked(); err != nil {
		// Roll back in-memory so disk and memory stay in sync.
		delete(t.items, td.ID)
		t.order = t.order[1:]
		return nil, err
	}
	t.publish()
	return td, nil
}

func (t *TodoStore) canonicalWorkspaceLocked(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	for _, td := range t.items {
		existing := strings.TrimSpace(td.Workspace)
		if existing != "" && strings.EqualFold(existing, workspace) {
			return existing
		}
	}
	return workspace
}

// Update mutates an existing todo. Pass nil for fields to leave alone.
// Returns the updated copy.
func (t *TodoStore) Update(id string, text *string, done *bool, taskID *string) (*Todo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	td, ok := t.items[id]
	if !ok {
		return nil, errors.New("todo not found")
	}
	prev := *td
	if text != nil {
		trimmed := strings.TrimSpace(*text)
		if trimmed == "" {
			return nil, errors.New("text cannot be empty")
		}
		if td.Text != trimmed {
			td.Text = trimmed
			td.DuplicateOf = ""
		}
	}
	if done != nil {
		if *done && !td.Done {
			td.DoneAt = time.Now().UnixMilli()
		} else if !*done {
			td.DoneAt = 0
		}
		td.Done = *done
	}
	if taskID != nil {
		td.TaskID = *taskID
	}
	if err := t.saveUnlocked(); err != nil {
		*td = prev
		return nil, err
	}
	t.publish()
	cp := *td
	return &cp, nil
}

// ApplyNormalization compare-and-sets the best-effort model enrichment. A
// user edit or completion always wins over a normalization that started from
// older text.
func (t *TodoStore) ApplyNormalization(id, expectText, newText, workspace, duplicateOf string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	td, ok := t.items[id]
	if !ok || td.Done || td.Text != expectText {
		return false, nil
	}
	prev := *td
	if newText != "" {
		td.Text = newText
	}
	if td.Workspace == "" && workspace != "" {
		td.Workspace = workspace
	}
	// A synchronous deterministic advisory is stronger than later model
	// enrichment. Async normalization may fill an empty advisory, but it must
	// never erase or replace one already persisted for the same text.
	if td.DuplicateOf == "" {
		td.DuplicateOf = duplicateOf
	}
	if td.OriginalText == "" && td.Text != expectText {
		td.OriginalText = expectText
	}
	if err := t.saveUnlocked(); err != nil {
		*td = prev
		return false, err
	}
	t.publish()
	return true, nil
}

// Delete removes a todo. No-op (returns nil) if it's already gone — the
// UI may double-fire on a slow click and there's nothing to recover.
func (t *TodoStore) Delete(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.items[id]; !ok {
		return nil
	}
	prevItem := t.items[id]
	prevOrder := append([]string(nil), t.order...)
	delete(t.items, id)
	for i, oid := range t.order {
		if oid == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	if err := t.saveUnlocked(); err != nil {
		t.items[id] = prevItem
		t.order = prevOrder
		return err
	}
	t.publish()
	return nil
}

func (t *TodoStore) publish() {
	if t.uiBus != nil {
		t.uiBus.publish(uiEvent{Type: "todos-changed", Ts: time.Now().UnixMilli()})
	}
}

func generateTodoID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "todo_" + hex.EncodeToString(b[:])
}
