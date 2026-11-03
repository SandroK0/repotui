package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type repo struct {
	name, path    string
	branch        string
	ahead, behind int
	hasUpstream   bool
	dirty         int
	busy          bool
	msg           string
	failed        bool
}

type branch struct {
	name   string
	remote bool
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
)

type model struct {
	root          string
	repos         []repo
	cursor        int
	mode          mode
	branches      []branch
	bcursor       int
	filter        string
	input         string
	search        string
	width, height int
	frame         int
	log           []logEntry
}

type statusMsg struct {
	idx int
	r   repo
}

type opDoneMsg struct {
	idx int
	op  string
	out string
	err error
}

type tickMsg struct{}

type spinMsg struct{}

type branchesMsg struct {
	idx  int
	list []branch
	err  error
}

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

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func findRepos(root string) []repo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var repos []repo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			repos = append(repos, repo{name: e.Name(), path: p})
		}
	}
	return repos
}

func loadStatus(idx int, r repo) tea.Cmd {
	return func() tea.Msg {
		out, err := git(r.path, "status", "--porcelain=v2", "--branch")
		if err != nil {
			r.msg, r.failed = lastLine(out), true
			return statusMsg{idx, r}
		}
		r.ahead, r.behind, r.dirty, r.hasUpstream = 0, 0, 0, false
		for _, line := range strings.Split(out, "\n") {
			switch {
			case strings.HasPrefix(line, "# branch.head "):
				r.branch = strings.TrimPrefix(line, "# branch.head ")
			case strings.HasPrefix(line, "# branch.upstream "):
				r.hasUpstream = true
			case strings.HasPrefix(line, "# branch.ab "):
				fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &r.ahead, &r.behind)
			case line != "" && !strings.HasPrefix(line, "#"):
				r.dirty++
			}
		}
		return statusMsg{idx, r}
	}
}

func runOp(idx int, path, op string, args ...string) tea.Cmd {
	return func() tea.Msg {
		out, err := git(path, args...)
		return opDoneMsg{idx: idx, op: op, out: out, err: err}
	}
}

func checkoutDefault(idx int, path string) tea.Cmd {
	return func() tea.Msg {
		def := ""
		if out, err := git(path, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
			def = strings.TrimPrefix(out, "origin/")
		} else {
			for _, b := range []string{"master", "main"} {
				if _, err := git(path, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
					def = b
					break
				}
			}
		}
		if def == "" {
			return opDoneMsg{idx: idx, op: "checkout default", err: fmt.Errorf("no default branch found")}
		}
		out, err := git(path, "checkout", def)
		return opDoneMsg{idx: idx, op: "checkout " + def, out: out, err: err}
	}
}

func loadBranches(idx int, path string) tea.Cmd {
	return func() tea.Msg {
		out, err := git(path, "for-each-ref", "--sort=-committerdate", "--format=%(refname)", "refs/heads", "refs/remotes")
		if err != nil {
			return branchesMsg{idx: idx, err: fmt.Errorf("%s", lastLine(out))}
		}
		seen := map[string]bool{}
		var local, remote []branch
		for _, ref := range strings.Split(out, "\n") {
			if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
				seen[name] = true
				local = append(local, branch{name: name})
			}
		}
		for _, ref := range strings.Split(out, "\n") {
			rest, ok := strings.CutPrefix(ref, "refs/remotes/")
			if !ok {
				continue
			}
			_, name, ok := strings.Cut(rest, "/")
			if !ok || name == "HEAD" || seen[name] {
				continue
			}
			seen[name] = true
			remote = append(remote, branch{name: name, remote: true})
		}
		return branchesMsg{idx: idx, list: append(local, remote...)}
	}
}

func stash(idx int, path string) tea.Cmd {
	message := "repotui " + time.Now().Format("2006-01-02 15:04")
	return runOp(idx, path, "stash", "stash", "push", "--include-untracked", "-m", message)
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

func (m model) matches(r repo) bool {
	q := strings.ToLower(m.search)
	return strings.Contains(strings.ToLower(r.name), q) || strings.Contains(strings.ToLower(r.branch), q)
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
	for i, r := range m.repos {
		if m.matches(r) {
			m.cursor = i
			return
		}
	}
}

func (m *model) forAll(build func(i int, r repo) tea.Cmd) tea.Cmd {
	var cmds []tea.Cmd
	for i, r := range m.repos {
		if r.busy || !m.matches(r) {
			continue
		}
		cmds = append(cmds, m.start(i, build(i, r)))
	}
	return tea.Batch(cmds...)
}

func (m model) filtered() []branch {
	if m.filter == "" {
		return m.branches
	}
	var out []branch
	for _, b := range m.branches {
		if strings.Contains(strings.ToLower(b.name), strings.ToLower(m.filter)) {
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
	for i, r := range m.repos {
		if !r.busy {
			cmds = append(cmds, loadStatus(i, r))
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
		r := msg.r
		r.busy = m.repos[msg.idx].busy
		r.msg, r.failed = m.repos[msg.idx].msg, m.repos[msg.idx].failed
		if msg.r.failed {
			r.msg, r.failed = msg.r.msg, true
		}
		m.repos[msg.idx] = r
	case opDoneMsg:
		r := &m.repos[msg.idx]
		r.busy = false
		if msg.err != nil {
			detail := msg.out
			if detail == "" {
				detail = msg.err.Error()
			}
			r.failed = true
			r.msg = msg.op + ": " + lastLine(detail)
		} else {
			r.msg = msg.op
		}
		m.addLog(r.name, r.msg, r.failed)
		return m, loadStatus(msg.idx, *r)
	case branchesMsg:
		if msg.err != nil {
			m.repos[msg.idx].msg, m.repos[msg.idx].failed = msg.err.Error(), true
			return m, nil
		}
		m.branches, m.bcursor, m.filter, m.mode = msg.list, 0, "", modeBranches
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
	r := m.repos[m.cursor]
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
	if !m.matches(r) {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "enter", "b":
		return m, loadBranches(m.cursor, r.path)
	case "p":
		if !r.busy {
			return m, m.start(m.cursor, runOp(m.cursor, r.path, "pull", "pull", "--ff-only"))
		}
	case "P":
		return m, m.forAll(func(i int, r repo) tea.Cmd { return runOp(i, r.path, "pull", "pull", "--ff-only") })
	case "f":
		if !r.busy {
			return m, m.start(m.cursor, runOp(m.cursor, r.path, "fetch", "fetch", "--prune"))
		}
	case "F":
		return m, m.forAll(func(i int, r repo) tea.Cmd { return runOp(i, r.path, "fetch", "fetch", "--prune") })
	case "m":
		if !r.busy {
			return m, m.start(m.cursor, checkoutDefault(m.cursor, r.path))
		}
	case "M":
		return m, m.forAll(func(i int, r repo) tea.Cmd { return checkoutDefault(i, r.path) })
	case "s":
		if r.busy {
			return m, nil
		}
		if r.dirty == 0 {
			m.addLog(r.name, "nothing to stash", false)
			return m, nil
		}
		return m, m.start(m.cursor, stash(m.cursor, r.path))
	case "S":
		var cmds []tea.Cmd
		for i, r := range m.repos {
			if !r.busy && r.dirty > 0 && m.matches(r) {
				cmds = append(cmds, m.start(i, stash(i, r.path)))
			}
		}
		return m, tea.Batch(cmds...)
	case "n":
		m.mode, m.input = modeNewBranch, ""
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
		b := list[m.bcursor]
		m.mode = modeList
		r := m.repos[m.cursor]
		return m, m.start(m.cursor, runOp(m.cursor, r.path, "checkout "+b.name, "checkout", b.name))
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
		r := m.repos[m.cursor]
		return m, m.start(m.cursor, runOp(m.cursor, r.path, "new branch "+name, "checkout", "-b", name))
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
		if line != "" && lipgloss.Width(line)+3+lipgloss.Width(item) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += dimStyle.Render(" · ")
		}
		line += item
	}
	return " " + strings.Join(append(lines, line), "\n ")
}

func (m model) header(w int) string {
	changed, behind := 0, 0
	for _, r := range m.repos {
		if r.dirty > 0 {
			changed++
		}
		if r.behind > 0 {
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
	left := badgeStyle.Render(" ⎇ repotui ") + dimStyle.Render("  "+tildify(m.root))
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(summary)-1)
	return left + strings.Repeat(" ", gap) + summary
}

func (m model) table(inner int) string {
	const syncW, chgW, stW = 9, 7, 1
	nameW := len("REPO")
	for _, r := range m.repos {
		nameW = max(nameW, len(r.name))
	}
	branchW := max(12, inner-nameW-syncW-chgW-stW-10)

	rows := []string{
		"  " + pad(headerStyle.Render("REPO"), nameW) + "  " + pad(headerStyle.Render("BRANCH"), branchW) + "  " +
			pad(headerStyle.Render("SYNC"), syncW) + "  " + pad(headerStyle.Render("CHANGES"), chgW),
		dimStyle.Render(strings.Repeat("─", inner)),
	}

	shown := 0
	for i, r := range m.repos {
		if !m.matches(r) {
			continue
		}
		shown++
		selected := i == m.cursor && (m.mode == modeList || m.mode == modeSearch)

		marker, name := "  ", r.name
		if selected {
			marker, name = accentStyle.Render("▌ "), accentStyle.Render(r.name)
		} else if i == m.cursor {
			name = lipgloss.NewStyle().Bold(true).Render(r.name)
		}

		bs := branchStyle
		if r.branch == "master" || r.branch == "main" {
			bs = mainStyle
		}
		br := bs.Render(truncate(r.branch, branchW))

		sync := dimStyle.Render("local")
		if r.hasUpstream {
			sync = okStyle.Render("✓ synced")
			if r.ahead > 0 || r.behind > 0 {
				sync = ""
				if r.ahead > 0 {
					sync += okStyle.Render(fmt.Sprintf("↑%d ", r.ahead))
				}
				if r.behind > 0 {
					sync += warnStyle.Render(fmt.Sprintf("↓%d", r.behind))
				}
			}
		}

		chg := dimStyle.Render("clean")
		if r.dirty > 0 {
			chg = warnStyle.Render(fmt.Sprintf("● %d", r.dirty))
		}

		status := " "
		switch {
		case r.busy:
			status = accentStyle.Render(spinner[m.frame%len(spinner)])
		case r.failed:
			status = errStyle.Render("✗")
		case r.msg != "":
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
	r := m.repos[m.cursor]
	lines := []string{
		accentStyle.Render("Switch branch") + dimStyle.Render(" · "+r.name),
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
		if b.remote {
			tag = dimStyle.Render("  remote")
		}
		if b.name == r.branch {
			tag = okStyle.Render("  ● current")
		}
		name := truncate(b.name, inner-lipgloss.Width(tag)-2)
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

func (m model) View() string {
	w := m.width
	if w == 0 {
		w = 100
	}
	inner := w - 4

	if len(m.repos) == 0 {
		return m.header(w) + "\n\n" + errStyle.Render(" No git repos found in "+tildify(m.root)) + "\n\n" + helpLine(w, "q", "quit") + "\n"
	}

	sections := []string{m.header(w), m.table(inner)}

	switch m.mode {
	case modeSearch:
		sections = append(sections,
			" "+accentStyle.Render("/")+" "+m.search+accentStyle.Render("█"),
			helpLine(w-2, "type", "filter by repo or branch", "↑↓", "move", "enter", "keep filter", "esc", "clear"))
	case modeList:
		if m.search != "" {
			sections = append(sections, " "+dimStyle.Render("filter: ")+accentStyle.Render(m.search)+dimStyle.Render("  (esc to clear, actions on all apply to these only)"))
		}
		if a := m.activity(inner); a != "" {
			sections = append(sections, a)
		}
		sections = append(sections, helpLine(w-2,
			"↑↓", "move", "/", "search", "enter", "branches", "p/P", "pull one/all", "f/F", "fetch one/all",
			"m/M", "master/main one/all", "s/S", "stash one/all", "n", "new branch", "r", "refresh", "q", "quit"))
	case modeNewBranch:
		box := accentStyle.Render("New branch") + dimStyle.Render(" · "+m.repos[m.cursor].name) + "\n" +
			accentStyle.Render("› ") + m.input + accentStyle.Render("█")
		sections = append(sections, pickerStyle.Width(inner+2).Render(box), helpLine(w-2, "enter", "create", "esc", "cancel"))
	case modeBranches:
		visible := max(5, m.height-len(m.repos)-16)
		sections = append(sections, m.picker(inner, visible),
			helpLine(w-2, "type", "filter", "↑↓", "move", "enter", "checkout", "esc", "back"))
	}
	return strings.Join(sections, "\n")
}

func main() {
	root := filepath.Join(os.Getenv("HOME"), "redberry", "plannr")
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	root, _ = filepath.Abs(root)
	repos := findRepos(root)
	sort.Slice(repos, func(i, j int) bool { return repos[i].name < repos[j].name })

	if _, err := tea.NewProgram(model{root: root, repos: repos}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
