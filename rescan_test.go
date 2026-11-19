package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRemeasure_SizeGoneAndNotDir(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	writeFile(t, filepath.Join(live, "a"), "12345")
	writeFile(t, filepath.Join(live, "sub", "b"), "678")
	nowFile := filepath.Join(root, "nowfile")
	writeFile(t, nowFile, "x")
	missing := filepath.Join(root, "missing")

	got := Remeasure([]string{live, missing, nowFile})

	if got[0].Path != live || got[0].Gone || got[0].Err != nil || got[0].Size != 8 {
		t.Errorf("live: got %+v, want size 8", got[0])
	}
	if got[0].LastModified.IsZero() {
		t.Errorf("live: LastModified not set")
	}
	if !got[1].Gone {
		t.Errorf("missing: want Gone, got %+v", got[1])
	}
	if !got[2].Gone {
		t.Errorf("file in place of dir: want Gone, got %+v", got[2])
	}
}

// newTestModel builds a list-state model from a real scan of root, with the
// cache directory redirected so tests never touch the user's cache.
func newTestModel(t *testing.T, root string) model {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	items, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	m := model{root: root, state: stateList}
	m.groups = buildGroups(items)
	m.rebuildRows()
	return m
}

func (m model) rowIndex(path string) int {
	for i, r := range m.rows {
		if r.item != nil && r.item.Path == path {
			return i
		}
	}
	return -1
}

func (m model) item(path string) *Item {
	if i := m.rowIndex(path); i >= 0 {
		return m.rows[i].item
	}
	return nil
}

func TestRescanItem_UpdatesSizeKeepsSelectionAndCursor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "package.json"), "{}")
	writeFile(t, filepath.Join(root, "a", "node_modules", "big"), strings.Repeat("x", 1000))
	writeFile(t, filepath.Join(root, "b", "package.json"), "{}")
	writeFile(t, filepath.Join(root, "b", "node_modules", "small"), strings.Repeat("x", 100))

	m := newTestModel(t, root)
	aPath, _ := filepath.Abs(filepath.Join(root, "a", "node_modules"))
	bPath, _ := filepath.Abs(filepath.Join(root, "b", "node_modules"))

	m.item(bPath).Selected = false
	m.cursor = m.rowIndex(aPath)

	// Shrink a/node_modules below b/node_modules, as a native clean would.
	if err := os.WriteFile(filepath.Join(aPath, "big"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, cmd := m.updateList(keyMsg("r"))
	m = next.(model)
	if _, busy := m.pending[aPath]; !busy {
		t.Fatalf("a/node_modules not marked pending")
	}
	if _, busy := m.pending[bPath]; busy {
		t.Fatalf("b/node_modules should not be rescanned")
	}
	m = runRescan(t, m, cmd)

	if got := m.item(aPath).Size; got != 1 {
		t.Errorf("a size: got %d want 1", got)
	}
	if got := m.item(bPath).Size; got != 100 {
		t.Errorf("b size changed: got %d want 100", got)
	}
	if m.item(bPath).Selected {
		t.Errorf("b selection was reset")
	}
	if m.rows[m.cursor].item == nil || m.rows[m.cursor].item.Path != aPath {
		t.Errorf("cursor did not follow a/node_modules after re-sort")
	}
	if m.rowIndex(aPath) < m.rowIndex(bPath) {
		t.Errorf("items not re-sorted by size")
	}
	if len(m.pending) != 0 {
		t.Errorf("pending not cleared: %v", m.pending)
	}
	if !strings.Contains(m.statusMessage, "-999 B") {
		t.Errorf("status message missing delta: %q", m.statusMessage)
	}
}

func TestRescanGroup_DropsVanishedItems(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "package.json"), "{}")
	writeFile(t, filepath.Join(root, "a", "node_modules", "f"), "xx")
	writeFile(t, filepath.Join(root, "b", "package.json"), "{}")
	writeFile(t, filepath.Join(root, "b", "node_modules", "f"), "xxx")
	writeFile(t, filepath.Join(root, "rs", "Cargo.toml"), "")
	writeFile(t, filepath.Join(root, "rs", "target", "f"), "x")

	m := newTestModel(t, root)
	aPath, _ := filepath.Abs(filepath.Join(root, "a", "node_modules"))
	rsPath, _ := filepath.Abs(filepath.Join(root, "rs", "target"))
	if err := os.RemoveAll(aPath); err != nil {
		t.Fatal(err)
	}
	// Removing the Rust target too must not affect a Node.js group rescan.
	if err := os.RemoveAll(rsPath); err != nil {
		t.Fatal(err)
	}

	for i, r := range m.rows {
		if r.kind == rowGroup && r.group.name == "Node.js" {
			m.cursor = i
		}
	}
	next, cmd := m.updateList(keyMsg("r"))
	m = runRescan(t, next.(model), cmd)

	if m.item(aPath) != nil {
		t.Errorf("vanished a/node_modules still listed")
	}
	if m.item(rsPath) == nil {
		t.Errorf("rs/target removed by a Node.js group rescan")
	}
	if r := m.rows[m.cursor]; r.kind != rowGroup || r.group.name != "Node.js" {
		t.Errorf("cursor left the Node.js header")
	}
	if !strings.Contains(m.statusMessage, "1 gone") {
		t.Errorf("status message missing gone count: %q", m.statusMessage)
	}
}

// runRescan executes the rescan command returned by updateList and feeds its
// result back into the model, skipping the spinner tick in the batch.
func runRescan(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command returned")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected a batch command")
	}
	for _, c := range batch {
		if msg, ok := c().(rescanDoneMsg); ok {
			next, _ := m.Update(msg)
			return next.(model)
		}
	}
	t.Fatal("batch had no rescan command")
	return m
}

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
