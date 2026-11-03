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
	left := badgeStyle.Render(" ⎇ repotui ") + dimStyle.Render("  "+tildify(m.root))
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(summary)-1)
	return left + strings.Repeat(" ", gap) + summary
}

func (m model) table(inner int) string {
	const syncW, chgW, stW = 9, 7, 1
	nameW := len("REPO")
	for _, v := range m.repos {
		nameW = max(nameW, len(v.repo.Name))
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

		chg := dimStyle.Render("clean")
		if s.Changes > 0 {
			chg = warnStyle.Render(fmt.Sprintf("● %d", s.Changes))
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
			"m/M", "master/main one/all", "s/S", "stash one/all", "n", "new branch", "r", "refresh", "q", "quit"))
	case modeNewBranch:
		box := accentStyle.Render("New branch") + dimStyle.Render(" · "+m.repos[m.cursor].repo.Name) + "\n" +
			accentStyle.Render("› ") + m.input + accentStyle.Render("█")
		sections = append(sections, pickerStyle.Width(inner+2).Render(box), helpLine(w, "enter", "create", "esc", "cancel"))
	case modeBranches:
		visible := max(5, m.height-len(m.repos)-16)
		sections = append(sections, m.picker(inner, visible),
			helpLine(w, "type", "filter", "↑↓", "move", "enter", "checkout", "esc", "back"))
	}
	return strings.Join(sections, "\n")
}

func main() {
	root := filepath.Join(os.Getenv("HOME"), "redberry", "plannr")
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	root, _ = filepath.Abs(root)

	var repos []repoView
	for _, r := range FindRepos(root) {
		repos = append(repos, repoView{repo: r})
	}

	if _, err := tea.NewProgram(model{root: root, repos: repos}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
