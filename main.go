package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// repoView is one row of the table: the latest git snapshot, refreshed on a timer, and the state of the last action run from the app.
type repoView struct {
	repo      Repo
	status    Status
	statusErr error
	busy      bool
	msg       string
	failed    bool
}

type logEntry struct {
	at     time.Time
	repo   string
	text   string
	failed bool
}

type mode int

const (
	modeList mode = iota
	modeBranches
	modeNewBranch
	modeSearch
	modeDiffFiles
	modeDiff
)

type model struct {
	root          string
	repos         []repoView
	cursor        int
	mode          mode
	branches      []Branch
	bcursor       int
	filter        string
	input         string
	search        string
	width, height int
	frame         int
	log           []logEntry
	files         []Change
	fcursor       int
	diff          []string
	dadd, ddel    int // added and removed lines in diff
	dscroll       int
	side          []sideRow
	split         bool // diff shown side by side; kept when moving between files
}

type statusMsg struct {
	idx    int
	status Status
	err    error
}

type actionDoneMsg struct {
	idx   int
	label string
	err   error
}

type branchesMsg struct {
	idx  int
	list []Branch
	err  error
}

type changesMsg struct {
	idx  int
	list []Change
	err  error
}

type diffMsg struct {
	idx   int
	file  int
	lines []string
	err   error
}

type tickMsg struct{}

type spinMsg struct{}

var (
	pink   = lipgloss.Color("212")
	teal   = lipgloss.Color("86")
	orange = lipgloss.Color("214")
	green  = lipgloss.Color("42")
	red    = lipgloss.Color("203")
	yellow = lipgloss.Color("221")
	grey   = lipgloss.Color("243")
	border = lipgloss.Color("238")

	badgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	headerStyle = lipgloss.NewStyle().Foreground(grey).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(grey)
	accentStyle = lipgloss.NewStyle().Foreground(pink).Bold(true)
	branchStyle = lipgloss.NewStyle().Foreground(teal)
	mainStyle   = lipgloss.NewStyle().Foreground(orange)
	okStyle     = lipgloss.NewStyle().Foreground(green)
	errStyle    = lipgloss.NewStyle().Foreground(red)
	warnStyle   = lipgloss.NewStyle().Foreground(yellow)
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	pickerStyle = boxStyle.BorderForeground(pink)

	spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

func loadStatus(idx int, r Repo) tea.Cmd {
	return func() tea.Msg {
		s, err := r.Status()
		return statusMsg{idx: idx, status: s, err: err}
	}
}

func loadBranches(idx int, r Repo) tea.Cmd {
	return func() tea.Msg {
		list, err := r.Branches()
		return branchesMsg{idx: idx, list: list, err: err}
	}
}

func loadChanges(idx int, r Repo) tea.Cmd {
	return func() tea.Msg {
		list, err := r.Changes()
		return changesMsg{idx: idx, list: list, err: err}
	}
}

func loadDiff(idx, file int, r Repo, c Change) tea.Cmd {
	return func() tea.Msg {
		lines, err := r.Diff(c)
		return diffMsg{idx: idx, file: file, lines: lines, err: err}
	}
}

// action runs a Repo call in the background; run returns the label shown in the activity panel.
func action(idx int, run func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		label, err := run()
		return actionDoneMsg{idx: idx, label: label, err: err}
	}
}

func pull(i int, r Repo) tea.Cmd {
	return action(i, func() (string, error) { return "pull", r.Pull() })
}

func fetch(i int, r Repo) tea.Cmd {
	return action(i, func() (string, error) { return "fetch", r.Fetch() })
}

func stash(i int, r Repo) tea.Cmd {
	return action(i, func() (string, error) { return "stash", r.Stash() })
}

func checkoutDefault(i int, r Repo) tea.Cmd {
	return action(i, func() (string, error) {
		name, err := r.CheckoutDefault()
		return "checkout " + name, err
	})
}

func (m *model) addLog(repo, text string, failed bool) {
	m.log = append(m.log, logEntry{at: time.Now(), repo: repo, text: text, failed: failed})
	if len(m.log) > 4 {
		m.log = m.log[len(m.log)-4:]
	}
}

func (m *model) start(idx int, cmd tea.Cmd) tea.Cmd {
	m.repos[idx].busy = true
	m.repos[idx].msg = ""
	m.repos[idx].failed = false
	return cmd
}

func (m model) matches(v repoView) bool {
	q := strings.ToLower(m.search)
	return strings.Contains(strings.ToLower(v.repo.Name), q) || strings.Contains(strings.ToLower(v.status.Branch), q)
}

func (m *model) moveCursor(dir int) {
	for i := m.cursor + dir; i >= 0 && i < len(m.repos); i += dir {
		if m.matches(m.repos[i]) {
			m.cursor = i
			return
		}
	}
}

func (m *model) fixCursor() {
	if m.matches(m.repos[m.cursor]) {
		return
	}
	for i, v := range m.repos {
		if m.matches(v) {
			m.cursor = i
			return
		}
	}
}

func (m *model) forAll(build func(i int, r Repo) tea.Cmd) tea.Cmd {
	var cmds []tea.Cmd
	for i, v := range m.repos {
		if v.busy || !m.matches(v) {
			continue
		}
		cmds = append(cmds, m.start(i, build(i, v.repo)))
	}
	return tea.Batch(cmds...)
}

func (m model) filtered() []Branch {
	if m.filter == "" {
		return m.branches
	}
	var out []Branch
	for _, b := range m.branches {
		if strings.Contains(strings.ToLower(b.Name), strings.ToLower(m.filter)) {
			out = append(out, b)
		}
	}
	return out
}

func tick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func spin() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m model) refresh() tea.Cmd {
	var cmds []tea.Cmd
	for i, v := range m.repos {
		if !v.busy {
			cmds = append(cmds, loadStatus(i, v.repo))
		}
	}
	return tea.Batch(cmds...)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(), spin())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case spinMsg:
		m.frame++
		return m, spin()
	case statusMsg:
		v := &m.repos[msg.idx]
		v.statusErr = msg.err
		if msg.err == nil {
			v.status = msg.status
		}
	case actionDoneMsg:
		v := &m.repos[msg.idx]
		v.busy, v.msg, v.failed = false, msg.label, msg.err != nil
		if msg.err != nil {
			v.msg = msg.label + ": " + msg.err.Error()
		}
		m.addLog(v.repo.Name, v.msg, v.failed)
		return m, loadStatus(msg.idx, v.repo)
	case branchesMsg:
		if msg.err != nil {
			m.addLog(m.repos[msg.idx].repo.Name, "branches: "+msg.err.Error(), true)
			return m, nil
		}
		m.branches, m.bcursor, m.filter, m.mode = msg.list, 0, "", modeBranches
	case changesMsg:
		// Ignore a late reply when the user has already moved on to something else.
		if msg.idx != m.cursor || (m.mode != modeList && m.mode != modeDiffFiles) {
			return m, nil
		}
		name := m.repos[msg.idx].repo.Name
		if msg.err != nil {
			m.addLog(name, "changes: "+msg.err.Error(), true)
			return m, nil
		}
		if len(msg.list) == 0 {
			m.mode = modeList
			m.addLog(name, "no changes", false)
			return m, nil
		}
		if m.mode == modeList {
			m.fcursor = 0
		}
		m.files, m.mode = msg.list, modeDiffFiles
		m.fcursor = min(m.fcursor, len(m.files)-1)
	case diffMsg:
		if msg.idx != m.cursor || (m.mode != modeDiffFiles && m.mode != modeDiff) {
			return m, nil
		}
		if msg.err != nil {
			m.addLog(m.repos[msg.idx].repo.Name, "diff: "+msg.err.Error(), true)
			return m, nil
		}
		m.fcursor, m.diff, m.dscroll, m.mode = msg.file, trimDiffHeader(msg.lines), 0, modeDiff
		m.side = sideBySide(m.diff)
		m.dadd, m.ddel = diffCounts(m.diff)
	case tea.KeyMsg:
		switch m.mode {
		case modeList:
			return m.updateList(msg)
		case modeBranches:
			return m.updateBranches(msg)
		case modeNewBranch:
			return m.updateNewBranch(msg)
		case modeSearch:
			return m.updateSearch(msg)
		case modeDiffFiles:
			return m.updateDiffFiles(msg)
		case modeDiff:
			return m.updateDiff(msg)
		}
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.repos) == 0 {
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	v := m.repos[m.cursor]
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "/":
		m.mode = modeSearch
		return m, nil
	case "esc":
		m.search = ""
		return m, nil
	case "r":
		return m, m.refresh()
	}
	if !m.matches(v) {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "enter", "b":
		return m, loadBranches(m.cursor, v.repo)
	case "n":
		m.mode, m.input = modeNewBranch, ""
	case "d":
		if v.status.Changes == 0 {
			m.addLog(v.repo.Name, "no changes", false)
			return m, nil
		}
		return m, loadChanges(m.cursor, v.repo)
	case "P":
		return m, m.forAll(pull)
	case "F":
		return m, m.forAll(fetch)
	case "M":
		return m, m.forAll(checkoutDefault)
	case "S":
		var cmds []tea.Cmd
		for i, v := range m.repos {
			if !v.busy && v.status.Changes > 0 && m.matches(v) {
				cmds = append(cmds, m.start(i, stash(i, v.repo)))
			}
		}
		return m, tea.Batch(cmds...)
	}

	if v.busy {
		return m, nil
	}
	switch msg.String() {
	case "p":
		return m, m.start(m.cursor, pull(m.cursor, v.repo))
	case "f":
		return m, m.start(m.cursor, fetch(m.cursor, v.repo))
	case "m":
		return m, m.start(m.cursor, checkoutDefault(m.cursor, v.repo))
	case "s":
		if v.status.Changes == 0 {
			m.addLog(v.repo.Name, "nothing to stash", false)
			return m, nil
		}
		return m, m.start(m.cursor, stash(m.cursor, v.repo))
	}
	return m, nil
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.search, m.mode = "", modeList
	case tea.KeyEnter:
		m.mode = modeList
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveCursor(-1)
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveCursor(1)
	case tea.KeyBackspace:
		if m.search != "" {
			m.search = m.search[:len(m.search)-1]
		}
	case tea.KeyRunes:
		m.search += string(msg.Runes)
	}
	m.fixCursor()
	return m, nil
}

func (m model) updateBranches(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	list := m.filtered()
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.mode = modeList
	case tea.KeyUp, tea.KeyCtrlP:
		m.bcursor = max(0, m.bcursor-1)
	case tea.KeyDown, tea.KeyCtrlN:
		m.bcursor = min(len(list)-1, m.bcursor+1)
	case tea.KeyEnter:
		if len(list) == 0 {
			return m, nil
		}
		name := list[m.bcursor].Name
		m.mode = modeList
		r := m.repos[m.cursor].repo
		return m, m.start(m.cursor, action(m.cursor, func() (string, error) { return "checkout " + name, r.Checkout(name) }))
	case tea.KeyBackspace:
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.bcursor = 0
		}
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
		m.bcursor = 0
	}
	return m, nil
}

func (m model) updateDiffFiles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.repos[m.cursor].repo
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.mode = modeList
	case "up", "k", "ctrl+p":
		m.fcursor = max(0, m.fcursor-1)
	case "down", "j", "ctrl+n":
		m.fcursor = min(len(m.files)-1, m.fcursor+1)
	case "r":
		return m, loadChanges(m.cursor, r)
	case "enter", "right", "l":
		return m, loadDiff(m.cursor, m.fcursor, r, m.files[m.fcursor])
	}
	return m, nil
}

// diffHeight is how many diff lines fit on screen below the header, the box's title and borders, and the key help.
func (m model) diffHeight() int {
	h := m.height
	if h == 0 {
		h = 24
	}
	return max(3, h-1-4-lipgloss.Height(m.diffHelp()))
}

func (m model) diffHelp() string {
	w := m.width
	if w == 0 {
		w = 100
	}
	layout := "side by side"
	if m.split {
		layout = "unified"
	}
	return helpLine(w, "↑↓/jk", "scroll", "space/b", "page", "g/G", "top/bottom", "n/p", "next/prev file", "s", layout, "esc", "files")
}

func (m model) diffLen() int {
	if m.split {
		return len(m.side)
	}
	return len(m.diff)
}

func (m model) updateDiff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := m.diffHeight()
	last := max(0, m.diffLen()-page)
	r := m.repos[m.cursor].repo
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "left", "h":
		m.mode = modeDiffFiles
	case "up", "k":
		m.dscroll--
	case "down", "j":
		m.dscroll++
	case " ", "pgdown", "ctrl+f":
		m.dscroll += page
	case "b", "pgup", "ctrl+b":
		m.dscroll -= page
	case "ctrl+d":
		m.dscroll += page / 2
	case "ctrl+u":
		m.dscroll -= page / 2
	case "g", "home":
		m.dscroll = 0
	case "G", "end":
		m.dscroll = last
	case "s":
		// Keep the same part of the file on screen when switching layouts.
		if m.split {
			if m.dscroll < len(m.side) {
				m.dscroll = m.side[m.dscroll].src
			}
		} else {
			top := m.dscroll
			m.dscroll = len(m.side)
			for i, r := range m.side {
				if r.src >= top {
					m.dscroll = i
					break
				}
			}
		}
		m.split = !m.split
		last = max(0, m.diffLen()-page)
	case "n", "]":
		if m.fcursor+1 < len(m.files) {
			return m, loadDiff(m.cursor, m.fcursor+1, r, m.files[m.fcursor+1])
		}
	case "p", "[":
		if m.fcursor > 0 {
			return m, loadDiff(m.cursor, m.fcursor-1, r, m.files[m.fcursor-1])
		}
	}
	m.dscroll = max(0, min(m.dscroll, last))
	return m, nil
}

// trimDiffHeader drops git's "diff --git", "index", "---" and "+++" lines, since the view already names the file,
// and expands tabs so every line's width is known.
func trimDiffHeader(lines []string) []string {
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			start = i
			break
		}
	}
	out := make([]string, 0, len(lines)-start)
	for _, l := range lines[start:] {
		out = append(out, strings.ReplaceAll(l, "\t", "    "))
	}
	return out
}

// diffCounts counts the added and removed lines of a diff whose file headers are already trimmed.
func diffCounts(lines []string) (added, deleted int) {
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			deleted++
		}
	}
	return added, deleted
}

type rowKind int

const (
	rowContext rowKind = iota
	rowChange
	rowHunk
	rowNote
)

// sideRow is one line of the side-by-side view. A side with a zero line number is blank.
type sideRow struct {
	kind        rowKind
	left, right string
	lnum, rnum  int
	src         int // index of the first unified line this row came from, to keep the scroll position when toggling
}

// sideBySide pairs a unified diff's lines into rows: context on both sides, and each run of removed lines next to
// the added lines that follow it.
func sideBySide(lines []string) []sideRow {
	var rows []sideRow
	var dels, adds []int
	ln, rn := 0, 0
	flush := func() {
		for i := 0; i < max(len(dels), len(adds)); i++ {
			r := sideRow{kind: rowChange, src: -1}
			if i < len(dels) {
				r.left, r.lnum, r.src = lines[dels[i]][1:], ln, dels[i]
				ln++
			}
			if i < len(adds) {
				r.right, r.rnum = lines[adds[i]][1:], rn
				if r.src < 0 {
					r.src = adds[i]
				}
				rn++
			}
			rows = append(rows, r)
		}
		dels, adds = nil, nil
	}
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "@@"):
			flush()
			fmt.Sscanf(l, "@@ -%d", &ln)
			if j := strings.Index(l, " +"); j >= 0 {
				fmt.Sscanf(l[j+2:], "%d", &rn)
			}
			rows = append(rows, sideRow{kind: rowHunk, left: l, src: i})
		case strings.HasPrefix(l, "-"):
			if len(adds) > 0 {
				flush()
			}
			dels = append(dels, i)
		case strings.HasPrefix(l, "+"):
			adds = append(adds, i)
		case strings.HasPrefix(l, "\\"):
			// "\ No newline at end of file" only makes sense in the unified view.
		case l == "" || strings.HasPrefix(l, " "):
			flush()
			text := strings.TrimPrefix(l, " ")
			rows = append(rows, sideRow{kind: rowContext, left: text, right: text, lnum: ln, rnum: rn, src: i})
			ln++
			rn++
		default:
			flush()
			rows = append(rows, sideRow{kind: rowNote, left: l, src: i})
		}
	}
	flush()
	return rows
}

func (m model) updateNewBranch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.mode = modeList
	case tea.KeyEnter:
		m.mode = modeList
		name := strings.TrimSpace(m.input)
		if name == "" {
			return m, nil
		}
		r := m.repos[m.cursor].repo
		return m, m.start(m.cursor, action(m.cursor, func() (string, error) { return "new branch " + name, r.CreateBranch(name) }))
	case tea.KeyBackspace:
		if m.input != "" {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	case tea.KeySpace:
		m.input += "-"
	}
	return m, nil
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func tildify(p string) string {
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func helpLine(width int, keys ...string) string {
	var lines []string
	line := ""
	for i := 0; i+1 < len(keys); i += 2 {
		item := accentStyle.Render(keys[i]) + " " + dimStyle.Render(keys[i+1])
		if line != "" && lipgloss.Width(line)+3+lipgloss.Width(item) > width-8 {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += dimStyle.Render(" · ")
		}
		line += item
	}
	lines = append(lines, line)
	for i, l := range lines {
		lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, l)
	}
	return strings.Join(lines, "\n")
}

func (m model) header(w int) string {
	changed, behind := 0, 0
	for _, v := range m.repos {
		if v.status.Changes > 0 {
			changed++
		}
		if v.status.Behind > 0 {
			behind++
		}
	}
	summary := dimStyle.Render(fmt.Sprintf("%d repos", len(m.repos)))
	if changed > 0 {
		summary += dimStyle.Render(" · ") + warnStyle.Render(fmt.Sprintf("%d changed", changed))
	}
	if behind > 0 {
		summary += dimStyle.Render(" · ") + warnStyle.Render(fmt.Sprintf("%d behind", behind))
	}
	badge := badgeStyle.Render(" ⎇ repotui ")
	pathW := w - lipgloss.Width(badge) - lipgloss.Width(summary) - 5
	left := badge + dimStyle.Render("  "+truncate(tildify(m.root), pathW))
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(summary)-1)
	return left + strings.Repeat(" ", gap) + summary
}

// changesCell is the table's CHANGES column: how many files changed and their total changed lines.
func changesCell(s Status) string {
	if s.Changes == 0 {
		return dimStyle.Render("clean")
	}
	cell := warnStyle.Render(fmt.Sprintf("● %d", s.Changes))
	if counts := lineCounts(s.Added, s.Deleted); counts != "" {
		cell += " " + counts
	}
	return cell
}

func (m model) table(inner int) string {
	const syncW, stW = 9, 1
	nameW, chgW := len("REPO"), len("CHANGES")
	for _, v := range m.repos {
		nameW = max(nameW, len(v.repo.Name))
		chgW = max(chgW, lipgloss.Width(changesCell(v.status)))
	}
	branchW := max(12, inner-nameW-syncW-chgW-stW-10)

	rows := []string{
		"  " + pad(headerStyle.Render("REPO"), nameW) + "  " + pad(headerStyle.Render("BRANCH"), branchW) + "  " +
			pad(headerStyle.Render("SYNC"), syncW) + "  " + pad(headerStyle.Render("CHANGES"), chgW),
		dimStyle.Render(strings.Repeat("─", inner)),
	}

	shown := 0
	for i, v := range m.repos {
		if !m.matches(v) {
			continue
		}
		shown++
		s := v.status
		selected := i == m.cursor && (m.mode == modeList || m.mode == modeSearch)

		marker, name := "  ", v.repo.Name
		if selected {
			marker, name = accentStyle.Render("▌ "), accentStyle.Render(v.repo.Name)
		} else if i == m.cursor {
			name = lipgloss.NewStyle().Bold(true).Render(v.repo.Name)
		}

		bs := branchStyle
		if s.Branch == "master" || s.Branch == "main" {
			bs = mainStyle
		}
		br := bs.Render(truncate(s.Branch, branchW))

		sync := dimStyle.Render("local")
		if s.HasUpstream {
			sync = okStyle.Render("✓ synced")
			if s.Ahead > 0 || s.Behind > 0 {
				sync = ""
				if s.Ahead > 0 {
					sync += okStyle.Render(fmt.Sprintf("↑%d ", s.Ahead))
				}
				if s.Behind > 0 {
					sync += warnStyle.Render(fmt.Sprintf("↓%d", s.Behind))
				}
			}
		}

		chg := changesCell(s)

		if v.statusErr != nil {
			br, sync, chg = errStyle.Render(truncate(v.statusErr.Error(), branchW)), dimStyle.Render("–"), dimStyle.Render("–")
		}

		status := " "
		switch {
		case v.busy:
			status = accentStyle.Render(spinner[m.frame%len(spinner)])
		case v.failed, v.statusErr != nil:
			status = errStyle.Render("✗")
		case v.msg != "":
			status = okStyle.Render("✓")
		}

		rows = append(rows, marker+pad(name, nameW)+"  "+pad(br, branchW)+"  "+pad(sync, syncW)+"  "+pad(chg, chgW)+"  "+status)
	}
	if shown == 0 {
		rows = append(rows, dimStyle.Render("  no repos match “"+m.search+"”"))
	}
	return boxStyle.Width(inner + 2).Render(strings.Join(rows, "\n"))
}

func (m model) activity(inner int) string {
	if len(m.log) == 0 {
		return ""
	}
	nameW := 0
	for _, e := range m.log {
		nameW = max(nameW, len(e.repo))
	}
	lines := []string{headerStyle.Render("ACTIVITY")}
	for i := len(m.log) - 1; i >= 0; i-- {
		e := m.log[i]
		icon, ts := okStyle.Render("✓"), okStyle
		if e.failed {
			icon, ts = errStyle.Render("✗"), errStyle
		}
		prefix := dimStyle.Render(e.at.Format("15:04:05")) + "  " + icon + " " + pad(e.repo, nameW) + "  "
		lines = append(lines, prefix+ts.Render(truncate(e.text, inner-lipgloss.Width(prefix))))
	}
	return boxStyle.Width(inner + 2).Render(strings.Join(lines, "\n"))
}

func (m model) picker(inner, visible int) string {
	list := m.filtered()
	v := m.repos[m.cursor]
	lines := []string{
		accentStyle.Render("Switch branch") + dimStyle.Render(" · "+v.repo.Name),
		accentStyle.Render("/ ") + m.filter + accentStyle.Render("█"),
		"",
	}
	start := 0
	if m.bcursor >= visible {
		start = m.bcursor - visible + 1
	}
	for i := start; i < len(list) && i < start+visible; i++ {
		b := list[i]
		tag := ""
		if b.Remote {
			tag = dimStyle.Render("  remote")
		}
		if b.Name == v.status.Branch {
			tag = okStyle.Render("  ● current")
		}
		name := truncate(b.Name, inner-lipgloss.Width(tag)-2)
		if i == m.bcursor {
			lines = append(lines, accentStyle.Render("▌ "+name)+tag)
		} else {
			lines = append(lines, "  "+name+tag)
		}
	}
	if len(list) == 0 {
		lines = append(lines, dimStyle.Render("  no matching branches"))
	} else {
		lines = append(lines, "", dimStyle.Render(fmt.Sprintf("  %d/%d", m.bcursor+1, len(list))))
	}
	return pickerStyle.Width(inner + 2).Render(strings.Join(lines, "\n"))
}

var (
	addStyle  = lipgloss.NewStyle().Foreground(green)
	delStyle  = lipgloss.NewStyle().Foreground(red)
	hunkStyle = lipgloss.NewStyle().Foreground(teal)
)

func changeCode(code string) string {
	switch code {
	case "A", "?":
		return addStyle.Render(code)
	case "D":
		return delStyle.Render(code)
	case "R":
		return hunkStyle.Render(code)
	}
	return warnStyle.Render(code)
}

func changeName(c Change) string {
	if c.OldPath != "" {
		return c.OldPath + " → " + c.Path
	}
	return c.Path
}

// lineCounts renders "+added -deleted" in diff colours, leaving out a side that is zero.
func lineCounts(added, deleted int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, addStyle.Render(fmt.Sprintf("+%d", added)))
	}
	if deleted > 0 {
		parts = append(parts, delStyle.Render(fmt.Sprintf("-%d", deleted)))
	}
	return strings.Join(parts, " ")
}

func changeCounts(c Change) string {
	if c.Binary {
		return dimStyle.Render("binary")
	}
	return lineCounts(c.Added, c.Deleted)
}

func (m model) fileList(inner, visible int) string {
	v := m.repos[m.cursor]
	added, deleted, countW := 0, 0, 0
	for _, c := range m.files {
		added += c.Added
		deleted += c.Deleted
		countW = max(countW, lipgloss.Width(changeCounts(c)))
	}
	summary := dimStyle.Render(fmt.Sprintf(" · %s · %d files", v.repo.Name, len(m.files)))
	if total := lineCounts(added, deleted); total != "" {
		summary += dimStyle.Render(" · ") + total
	}
	lines := []string{accentStyle.Render("Changes") + summary, ""}
	start := 0
	if m.fcursor >= visible {
		start = m.fcursor - visible + 1
	}
	nameW := inner - 4 - countW - 2
	for i := start; i < len(m.files) && i < start+visible; i++ {
		c := m.files[i]
		name := truncate(changeName(c), nameW)
		counts := changeCounts(c)
		counts = strings.Repeat(" ", countW-lipgloss.Width(counts)) + counts
		if i == m.fcursor {
			lines = append(lines, accentStyle.Render("▌ ")+changeCode(c.Code)+" "+accentStyle.Render(pad(name, nameW))+"  "+counts)
		} else {
			lines = append(lines, "  "+changeCode(c.Code)+" "+pad(name, nameW)+"  "+counts)
		}
	}
	return pickerStyle.Width(inner + 2).Render(strings.Join(lines, "\n"))
}

func (m model) diffView(inner int) string {
	c := m.files[m.fcursor]
	page := m.diffHeight()
	total := m.diffLen()
	end := min(total, m.dscroll+page)
	pos := "all"
	if total > page {
		pos = fmt.Sprintf("%d%%", end*100/total)
	}
	counts := lineCounts(m.dadd, m.ddel)
	if counts != "" {
		counts = dimStyle.Render(" · ") + counts
	}
	title := accentStyle.Render(truncate(changeName(c), inner-30-lipgloss.Width(counts))) + counts +
		dimStyle.Render(fmt.Sprintf(" · %s · file %d/%d · %s", m.repos[m.cursor].repo.Name, m.fcursor+1, len(m.files), pos))
	lines := []string{title, ""}
	if m.split {
		for _, r := range m.side[m.dscroll:end] {
			lines = append(lines, sideLine(r, inner))
		}
		return pickerStyle.Width(inner + 2).Render(strings.Join(lines, "\n"))
	}
	for _, l := range m.diff[m.dscroll:end] {
		l = truncate(l, inner)
		switch {
		case strings.HasPrefix(l, "@@"):
			l = hunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			l = addStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			l = delStyle.Render(l)
		case l != "" && !strings.HasPrefix(l, " "):
			l = dimStyle.Render(l)
		}
		lines = append(lines, l)
	}
	return pickerStyle.Width(inner + 2).Render(strings.Join(lines, "\n"))
}

// sideLine renders one side-by-side row as two columns of "line number, text" split by a rule.
func sideLine(r sideRow, inner int) string {
	switch r.kind {
	case rowHunk:
		return hunkStyle.Render(truncate(r.left, inner))
	case rowNote:
		return dimStyle.Render(truncate(r.left, inner))
	}
	const numW = 4
	colW := (inner - 3) / 2
	textW := colW - numW - 1
	cell := func(num int, text string, style lipgloss.Style) string {
		if num == 0 {
			return strings.Repeat(" ", colW)
		}
		return dimStyle.Render(fmt.Sprintf("%*d", numW, num)) + " " + style.Render(pad(truncate(text, textW), textW))
	}
	ls, rs := lipgloss.NewStyle(), lipgloss.NewStyle()
	if r.kind == rowChange {
		ls, rs = delStyle, addStyle
	}
	return cell(r.lnum, r.left, ls) + dimStyle.Render(" │ ") + cell(r.rnum, r.right, rs)
}

func (m model) View() string {
	w := m.width
	if w == 0 {
		w = 100
	}
	inner := w - 4

	if len(m.repos) == 0 {
		return m.header(w) + "\n\n" + errStyle.Render(" No git repos found in "+tildify(m.root)) + "\n\n" + helpLine(w, "q", "quit") + "\n"
	}

	if m.mode == modeDiff {
		return strings.Join([]string{m.header(w), m.diffView(inner), m.diffHelp()}, "\n")
	}

	sections := []string{m.header(w), m.table(inner)}

	switch m.mode {
	case modeSearch:
		sections = append(sections,
			" "+accentStyle.Render("/")+" "+m.search+accentStyle.Render("█"),
			helpLine(w, "type", "filter by repo or branch", "↑↓", "move", "enter", "keep filter", "esc", "clear"))
	case modeList:
		if m.search != "" {
			sections = append(sections, " "+dimStyle.Render("filter: ")+accentStyle.Render(m.search)+dimStyle.Render("  (esc to clear, actions on all apply to these only)"))
		}
		if a := m.activity(inner); a != "" {
			sections = append(sections, a)
		}
		sections = append(sections, helpLine(w,
			"↑↓", "move", "/", "search", "enter", "branches", "p/P", "pull one/all", "f/F", "fetch one/all",
			"m/M", "master/main one/all", "s/S", "stash one/all", "n", "new branch", "d", "diff", "r", "refresh", "q", "quit"))
	case modeNewBranch:
		box := accentStyle.Render("New branch") + dimStyle.Render(" · "+m.repos[m.cursor].repo.Name) + "\n" +
			accentStyle.Render("› ") + m.input + accentStyle.Render("█")
		sections = append(sections, pickerStyle.Width(inner+2).Render(box), helpLine(w, "enter", "create", "esc", "cancel"))
	case modeBranches:
		visible := max(5, m.height-len(m.repos)-16)
		sections = append(sections, m.picker(inner, visible),
			helpLine(w, "type", "filter", "↑↓", "move", "enter", "checkout", "esc", "back"))
	case modeDiffFiles:
		visible := max(5, m.height-len(m.repos)-14)
		sections = append(sections, m.fileList(inner, visible),
			helpLine(w, "↑↓/jk", "move", "enter", "view diff", "r", "reload", "esc", "back"))
	}
	return strings.Join(sections, "\n")
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	root, _ = filepath.Abs(root)

	found, err := LoadRepos(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repotui:", err)
		os.Exit(1)
	}
	var repos []repoView
	for _, r := range found {
		repos = append(repos, repoView{repo: r})
	}

	if _, err := tea.NewProgram(model{root: root, repos: repos}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
