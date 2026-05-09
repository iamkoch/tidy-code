package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type appState int

const (
	stateScanning appState = iota
	stateList
	stateConfirm
	stateDeleting
)

type group struct {
	name     string
	items    []*Item
	expanded bool
}

func (g *group) totalSize() int64 {
	var t int64
	for _, it := range g.items {
		t += it.Size
	}
	return t
}

func (g *group) selectedSize() int64 {
	var t int64
	for _, it := range g.items {
		if it.Selected {
			t += it.Size
		}
	}
	return t
}

func (g *group) allSelected() bool {
	for _, it := range g.items {
		if !it.Selected {
			return false
		}
	}
	return true
}

func (g *group) anySelected() bool {
	for _, it := range g.items {
		if it.Selected {
			return true
		}
	}
	return false
}

type rowKind int

const (
	rowGroup rowKind = iota
	rowItem
)

type row struct {
	kind  rowKind
	group *group
	item  *Item
}

type model struct {
	state         appState
	spinner       spinner.Model
	groups        []*group
	rows          []row
	cursor        int
	root          string
	width         int
	height        int
	scanErr       error
	statusMessage string
	failedPaths   []string
	cachedAt      time.Time // zero when results are fresh
}

type scanDoneMsg struct {
	items []Item
	err   error
}

type deleteResultMsg struct {
	freed        int64
	deletedPaths map[string]struct{}
	failed       []string
}

func newModel(root string) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := model{spinner: s, root: root}

	if cf, ok := loadCache(root); ok {
		// Items came from JSON; reset transient UI state.
		for i := range cf.Items {
			cf.Items[i].Selected = true
		}
		m.groups = buildGroups(cf.Items)
		m.rebuildRows()
		m.state = stateList
		m.cachedAt = cf.ScannedAt
		return m
	}

	m.state = stateScanning
	return m
}

func (m model) Init() tea.Cmd {
	if m.state == stateScanning {
		return tea.Batch(m.spinner.Tick, scanCmd(m.root))
	}
	return nil
}

// flatItems returns the current items as values (not pointers), suitable for
// serialising to the cache.
func (m model) flatItems() []Item {
	var out []Item
	for _, g := range m.groups {
		for _, it := range g.items {
			out = append(out, *it)
		}
	}
	return out
}

func scanCmd(root string) tea.Cmd {
	return func() tea.Msg {
		items, err := Scan(root)
		return scanDoneMsg{items: items, err: err}
	}
}

// deleteCmd deletes ONLY items where Selected == true. Anything passed in
// without Selected set is ignored as a defence-in-depth check; callers should
// already have filtered. Only successfully removed paths end up in
// deletedPaths. Failures stay on disk and remain in the model.
func deleteCmd(items []*Item) tea.Cmd {
	return func() tea.Msg {
		result := deleteResultMsg{deletedPaths: make(map[string]struct{})}
		for _, it := range items {
			if !it.Selected {
				continue
			}
			if err := os.RemoveAll(it.Path); err != nil {
				result.failed = append(result.failed, it.Path+": "+err.Error())
				continue
			}
			result.deletedPaths[it.Path] = struct{}{}
			result.freed += it.Size
		}
		return result
	}
}

func buildGroups(items []Item) []*group {
	byLang := map[string]*group{}
	for i := range items {
		it := &items[i]
		g, ok := byLang[it.Language]
		if !ok {
			g = &group{name: it.Language, expanded: true}
			byLang[it.Language] = g
		}
		g.items = append(g.items, it)
	}
	out := make([]*group, 0, len(byLang))
	for _, g := range byLang {
		sort.Slice(g.items, func(i, j int) bool {
			if g.items[i].Size != g.items[j].Size {
				return g.items[i].Size > g.items[j].Size
			}
			return g.items[i].Path < g.items[j].Path
		})
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].totalSize() > out[j].totalSize()
	})
	return out
}

func (m *model) rebuildRows() {
	m.rows = m.rows[:0]
	for _, g := range m.groups {
		m.rows = append(m.rows, row{kind: rowGroup, group: g})
		if g.expanded {
			for _, it := range g.items {
				m.rows = append(m.rows, row{kind: rowItem, group: g, item: it})
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// currentGroup returns the group the cursor is currently inside, whether the
// cursor sits on the group header row or on one of the group's item rows.
func (m model) currentGroup() *group {
	if len(m.rows) == 0 {
		return nil
	}
	return m.rows[m.cursor].group
}

// cursorTo moves the cursor to the row containing the given group's header.
func (m *model) cursorTo(g *group) {
	for i, r := range m.rows {
		if r.kind == rowGroup && r.group == g {
			m.cursor = i
			return
		}
	}
}

// jumpToGroup moves the cursor to the next/previous group header.
// dir = +1 forward, -1 backward.
func (m *model) jumpToGroup(dir int) {
	if len(m.rows) == 0 {
		return
	}
	step := dir
	for i := m.cursor + step; i >= 0 && i < len(m.rows); i += step {
		if m.rows[i].kind == rowGroup {
			m.cursor = i
			return
		}
	}
}

func (m model) viewportHeight() int {
	h := m.height - 8
	if h < 1 {
		h = 10
	}
	return h
}

func (m model) selectedItems() []*Item {
	var out []*Item
	for _, g := range m.groups {
		for _, it := range g.items {
			if it.Selected {
				out = append(out, it)
			}
		}
	}
	return out
}

func (m model) totalSelected() (count int, size int64) {
	for _, g := range m.groups {
		for _, it := range g.items {
			if it.Selected {
				count++
				size += it.Size
			}
		}
	}
	return
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.state == stateScanning || m.state == stateDeleting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case scanDoneMsg:
		if msg.err != nil {
			m.scanErr = msg.err
		}
		m.groups = buildGroups(msg.items)
		m.rebuildRows()
		m.state = stateList
		m.cachedAt = time.Time{} // fresh
		_ = saveCache(m.root, msg.items, time.Now())
		return m, nil

	case deleteResultMsg:
		// Drop only paths that were ACTUALLY removed. Failed deletes stay on
		// disk and stay visible in the model (still selected) so the user can
		// retry or investigate.
		for _, g := range m.groups {
			kept := g.items[:0]
			for _, it := range g.items {
				if _, ok := msg.deletedPaths[it.Path]; ok {
					continue
				}
				kept = append(kept, it)
			}
			g.items = kept
		}
		gs := m.groups[:0]
		for _, g := range m.groups {
			if len(g.items) > 0 {
				gs = append(gs, g)
			}
		}
		m.groups = gs
		m.rebuildRows()
		m.failedPaths = msg.failed
		m.statusMessage = fmt.Sprintf("freed %s across %d items",
			humanBytes(msg.freed), len(msg.deletedPaths))
		if len(msg.failed) > 0 {
			m.statusMessage += fmt.Sprintf("  (%d failed)", len(msg.failed))
		}
		m.state = stateList
		// Persist updated state so a quit-then-rerun reflects the deletion.
		_ = saveCache(m.root, m.flatItems(), time.Now())
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateList:
			return m.updateList(msg)
		case stateConfirm:
			return m.updateConfirm(msg)
		}
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	// --- movement ---
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.rows) - 1
	case "pgdown", "ctrl+d":
		m.cursor += m.viewportHeight()
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
		}
	case "pgup", "ctrl+u":
		m.cursor -= m.viewportHeight()
		if m.cursor < 0 {
			m.cursor = 0
		}
	case "tab", "]":
		m.jumpToGroup(+1)
	case "shift+tab", "[":
		m.jumpToGroup(-1)

	// --- folding ---
	case "c", "left", "h":
		if g := m.currentGroup(); g != nil {
			g.expanded = false
			m.rebuildRows()
			m.cursorTo(g)
		}
	case "o", "right", "l":
		if g := m.currentGroup(); g != nil {
			g.expanded = true
			m.rebuildRows()
			m.cursorTo(g)
		}
	case "C":
		for _, g := range m.groups {
			g.expanded = false
		}
		m.rebuildRows()
	case "O":
		for _, g := range m.groups {
			g.expanded = true
		}
		m.rebuildRows()
	case "enter":
		if len(m.rows) == 0 {
			return m, nil
		}
		r := m.rows[m.cursor]
		if r.kind == rowGroup {
			r.group.expanded = !r.group.expanded
			m.rebuildRows()
		}

	// --- selection ---
	case " ", "x":
		if len(m.rows) == 0 {
			return m, nil
		}
		r := m.rows[m.cursor]
		if r.kind == rowGroup {
			target := !r.group.allSelected()
			for _, it := range r.group.items {
				it.Selected = target
			}
		} else {
			r.item.Selected = !r.item.Selected
		}
	case "a":
		for _, g := range m.groups {
			for _, it := range g.items {
				it.Selected = true
			}
		}
	case "n":
		for _, g := range m.groups {
			for _, it := range g.items {
				it.Selected = false
			}
		}

	// --- action ---
	case "d":
		count, _ := m.totalSelected()
		if count > 0 {
			m.state = stateConfirm
		}
	case "r":
		m.state = stateScanning
		m.groups = nil
		m.rows = nil
		m.cursor = 0
		m.statusMessage = ""
		m.cachedAt = time.Time{}
		return m, tea.Batch(m.spinner.Tick, scanCmd(m.root))
	}
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.state = stateDeleting
		return m, tea.Batch(m.spinner.Tick, deleteCmd(m.selectedItems()))
	case "n", "N", "esc", "q":
		m.state = stateList
	}
	return m, nil
}

// ---------- View ----------

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	groupStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	cursorStyle   = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))

	ageRedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	ageAmberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	ageGreenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
)

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

// ageStyle returns the colour for a last-modified timestamp:
//   - red:   modified within the past week (recently active, risky to delete)
//   - amber: 1–4 weeks old
//   - green: older than 4 weeks (stale, safe to delete)
func ageStyle(t time.Time) lipgloss.Style {
	if t.IsZero() {
		return dimStyle
	}
	d := time.Since(t)
	switch {
	case d < 7*24*time.Hour:
		return ageRedStyle
	case d < 28*24*time.Hour:
		return ageAmberStyle
	default:
		return ageGreenStyle
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (m model) View() string {
	switch m.state {
	case stateScanning:
		return fmt.Sprintf("\n  %s scanning %s for build artefacts...\n\n", m.spinner.View(), m.root)
	case stateDeleting:
		return fmt.Sprintf("\n  %s deleting...\n\n", m.spinner.View())
	case stateConfirm:
		count, size := m.totalSelected()
		return fmt.Sprintf(
			"\n  %s\n\n  Delete %d items totalling %s?\n  This cannot be undone.\n\n  %s\n",
			titleStyle.Render("tidy"),
			count, humanBytes(size),
			warnStyle.Render("[y] yes   [n] no"),
		)
	}

	return m.viewList()
}

func (m model) viewList() string {
	var b strings.Builder

	count, size := m.totalSelected()
	header := fmt.Sprintf("%s   %s",
		titleStyle.Render("tidy"),
		dimStyle.Render(m.root),
	)
	totals := fmt.Sprintf("selected: %s across %d items",
		selectedStyle.Render(humanBytes(size)),
		count,
	)
	b.WriteString("\n  " + header + "\n")
	b.WriteString("  " + totals + "\n")
	if !m.cachedAt.IsZero() {
		b.WriteString("  " + dimStyle.Render(
			fmt.Sprintf("cached %s · press [r] to rescan", humanAge(m.cachedAt)),
		) + "\n")
	}
	if m.statusMessage != "" {
		b.WriteString("  " + selectedStyle.Render(m.statusMessage) + "\n")
	}
	b.WriteString("\n")

	if len(m.rows) == 0 {
		b.WriteString("  " + dimStyle.Render("nothing to clean up.") + "\n\n")
		b.WriteString("  " + helpStyle.Render("[q] quit") + "\n")
		return b.String()
	}

	// Viewport: simple windowing around cursor.
	maxRows := m.viewportHeight()
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.rows) {
		end = len(m.rows)
	}

	for i := start; i < end; i++ {
		r := m.rows[i]
		line := renderRow(r)
		if i == m.cursor {
			line = cursorStyle.Render("▸ " + line)
		} else {
			line = "  " + line
		}
		b.WriteString("  " + line + "\n")
	}

	if start > 0 || end < len(m.rows) {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("showing %d–%d of %d", start+1, end, len(m.rows))) + "\n")
	}

	help1 := "[↑↓/jk] move  [PgUp/PgDn] page  [g/G] top/bot  [Tab/[ ]] next/prev group"
	help2 := "[c/o or ←/→] fold  [C/O] fold all  [space] toggle  [a/n] all/none  [d] delete  [r] rescan  [q] quit"
	b.WriteString("\n  " + helpStyle.Render(help1) + "\n")
	b.WriteString("  " + helpStyle.Render(help2) + "\n")
	return b.String()
}

func renderRow(r row) string {
	switch r.kind {
	case rowGroup:
		mark := checkbox(r.group.allSelected(), r.group.anySelected())
		arrow := "▸"
		if r.group.expanded {
			arrow = "▾"
		}
		return fmt.Sprintf("%s %s %s  %s  %s",
			mark,
			arrow,
			groupStyle.Render(r.group.name),
			dimStyle.Render(fmt.Sprintf("(%d)", len(r.group.items))),
			dimStyle.Render(humanBytes(r.group.selectedSize())+" / "+humanBytes(r.group.totalSize())),
		)
	case rowItem:
		mark := checkbox(r.item.Selected, false)
		size := humanBytes(r.item.Size)
		age := padRight(humanAge(r.item.LastModified), 10)
		ageColoured := ageStyle(r.item.LastModified).Render(age)
		return fmt.Sprintf("    %s %s  %s  %s",
			mark,
			padRight(size, 10),
			ageColoured,
			r.item.Path,
		)
	}
	return ""
}

func checkbox(full, partial bool) string {
	switch {
	case full:
		return selectedStyle.Render("[x]")
	case partial:
		return warnStyle.Render("[~]")
	default:
		return dimStyle.Render("[ ]")
	}
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	abs, err := os.Getwd()
	if err == nil && root == "." {
		root = abs
	}

	p := tea.NewProgram(newModel(root), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
