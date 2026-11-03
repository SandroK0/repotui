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

type mode int

const (
	modeList mode = iota
	modeBranches
	modeNewBranch
)

type model struct {
	root     string
	repos    []repo
	cursor   int
	mode     mode
	branches []branch
	bcursor  int
	filter   string
	input    string
	height   int
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

type branchesMsg struct {
	idx  int
	list []branch
	err  error
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	mainStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
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

func (m *model) start(idx int, cmd tea.Cmd) tea.Cmd {
	m.repos[idx].busy = true
	m.repos[idx].msg = ""
	m.repos[idx].failed = false
	return cmd
}

func (m *model) forAll(build func(i int, r repo) tea.Cmd) tea.Cmd {
	var cmds []tea.Cmd
	for i, r := range m.repos {
		if r.busy {
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
	return tea.Batch(m.refresh(), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
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
			r.failed = true
			detail := msg.out
			if detail == "" {
				detail = msg.err.Error()
			}
			r.msg = msg.op + ": " + lastLine(detail)
		} else {
			r.msg = msg.op + " ✓"
		}
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
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.repos)-1, m.cursor+1)
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
	case "n":
		m.mode, m.input = modeNewBranch, ""
	case "r":
		return m, m.refresh()
	}
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
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("repotui") + dimStyle.Render("  "+m.root) + "\n\n")

	if len(m.repos) == 0 {
		b.WriteString(errStyle.Render("No git repos found in "+m.root) + "\n\nq quit\n")
		return b.String()
	}

	nameW := 0
	for _, r := range m.repos {
		nameW = max(nameW, len(r.name))
	}

	for i, r := range m.repos {
		bs := branchStyle
		if r.branch == "master" || r.branch == "main" {
			bs = mainStyle
		}
		sync := dimStyle.Render("no upstream")
		if r.hasUpstream {
			sync = okStyle.Render("in sync")
			if r.ahead > 0 || r.behind > 0 {
				sync = warnStyle.Render(fmt.Sprintf("↑%d ↓%d", r.ahead, r.behind))
			}
		}
		dirty := dimStyle.Render("clean")
		if r.dirty > 0 {
			dirty = warnStyle.Render(fmt.Sprintf("✎ %d", r.dirty))
		}
		status := ""
		switch {
		case r.busy:
			status = dimStyle.Render("working…")
		case r.failed:
			status = errStyle.Render(truncate(r.msg, 70))
		case r.msg != "":
			status = okStyle.Render(r.msg)
		}

		line := fmt.Sprintf("%-*s  %s  %s  %s  %s",
			nameW, r.name,
			bs.Render(fmt.Sprintf("%-50s", truncate(r.branch, 50))),
			lipgloss.NewStyle().Width(11).Render(sync),
			lipgloss.NewStyle().Width(7).Render(dirty),
			status,
		)
		if i == m.cursor && m.mode == modeList {
			line = selStyle.Render("▸ ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}

	switch m.mode {
	case modeList:
		b.WriteString("\n" + dimStyle.Render("↑/↓ move • enter branches • p/P pull (one/all) • f/F fetch • m/M checkout master/main • n new branch • r refresh • q quit") + "\n")
	case modeNewBranch:
		b.WriteString("\n" + titleStyle.Render("New branch in "+m.repos[m.cursor].name+": ") + m.input + "█\n")
		b.WriteString(dimStyle.Render("enter create • esc cancel") + "\n")
	case modeBranches:
		list := m.filtered()
		b.WriteString("\n" + titleStyle.Render("Checkout in "+m.repos[m.cursor].name) + "  filter: " + m.filter + "█\n")
		visible := max(5, m.height-len(m.repos)-9)
		start := 0
		if m.bcursor >= visible {
			start = m.bcursor - visible + 1
		}
		for i := start; i < len(list) && i < start+visible; i++ {
			br := list[i]
			label := br.name
			if br.remote {
				label += dimStyle.Render("  (remote)")
			}
			if br.name == m.repos[m.cursor].branch {
				label += okStyle.Render("  ● current")
			}
			if i == m.bcursor {
				b.WriteString(selStyle.Render("▸ ") + label + "\n")
			} else {
				b.WriteString("  " + label + "\n")
			}
		}
		if len(list) == 0 {
			b.WriteString(dimStyle.Render("  no matches") + "\n")
		}
		b.WriteString(dimStyle.Render("type to filter • ↑/↓ move • enter checkout • esc back") + "\n")
	}
	return b.String()
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
