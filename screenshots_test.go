//go:build screenshots

// Regenerates the README screenshots against a folder of fake repos:
//
//	go test -tags screenshots -run TestScreenshots .
//
// Needs freeze (https://github.com/charmbracelet/freeze) on PATH.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type showcaseRepo struct {
	name     string
	branches []string
	checkout string
	ahead    int
	behind   int
	changes  int
	local    bool
}

var showcase = []showcaseRepo{
	{name: "auth-service", branches: []string{"feat/oauth-scopes", "fix/token-refresh", "chore/bump-deps"}, checkout: "feat/oauth-scopes", ahead: 2},
	{name: "billing-service", checkout: "main", behind: 3},
	{name: "design-system", branches: []string{"fix/button-focus-ring"}, checkout: "fix/button-focus-ring", changes: 4},
	{name: "docs", checkout: "main"},
	{name: "mobile-app", branches: []string{"release/2.4.0"}, checkout: "release/2.4.0", ahead: 1, behind: 2},
	{name: "notification-service", checkout: "main", changes: 1},
	{name: "web-dashboard", checkout: "spike/new-charts", local: true},
}

func buildShowcase(t *testing.T) string {
	isolateGit(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "acme")
	os.Mkdir(root, 0o755)

	for _, s := range showcase {
		origin := filepath.Join(dir, "origins", s.name+".git")
		upstream := filepath.Join(dir, "upstreams", s.name)
		clone := filepath.Join(root, s.name)

		run(t, dir, "init", "-q", "--bare", "-b", "main", origin)
		run(t, dir, "clone", "-q", origin, upstream)
		commit(t, upstream, "README.md")
		run(t, upstream, "push", "-q", "origin", "main")
		for _, b := range s.branches {
			run(t, upstream, "checkout", "-q", "-b", b, "main")
			commit(t, upstream, strings.ReplaceAll(b, "/", "-"))
			run(t, upstream, "push", "-q", "origin", b)
		}
		run(t, dir, "clone", "-q", origin, clone)

		if s.local {
			run(t, clone, "checkout", "-q", "-b", s.checkout)
		} else {
			run(t, clone, "checkout", "-q", s.checkout)
		}
		for i := range s.behind {
			run(t, upstream, "checkout", "-q", s.checkout)
			commit(t, upstream, "upstream-"+string(rune('a'+i)))
			run(t, upstream, "push", "-q", "origin", s.checkout)
		}
		for i := range s.ahead {
			commit(t, clone, "local-"+string(rune('a'+i)))
		}
		run(t, clone, "fetch", "-q")
		for i := range s.changes {
			os.WriteFile(filepath.Join(clone, "wip-"+string(rune('a'+i))), []byte("wip"), 0o644)
		}
	}
	return root
}

// drive feeds a command's messages back into the model until nothing is left to run.
func drive(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = drive(m, c)
		}
		return m
	}
	m, next := m.Update(msg)
	return drive(m, next)
}

func press(m tea.Model, keys ...string) tea.Model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		m = drive(m, cmd)
	}
	return m
}

func newShowcaseModel(t *testing.T, root string) tea.Model {
	var repos []repoView
	for _, r := range FindRepos(root) {
		repos = append(repos, repoView{repo: r})
	}
	var m tea.Model = model{root: filepath.Join(os.Getenv("HOME"), "code", "acme"), repos: repos}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	return drive(m, m.(model).refresh())
}

func TestScreenshots(t *testing.T) {
	if _, err := exec.LookPath("freeze"); err != nil {
		t.Skip("freeze is not installed")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	root := buildShowcase(t)

	shots := map[string]tea.Model{
		"overview": press(newShowcaseModel(t, root), "F", "j", "j", "j", "j", "p", "k", "k", "k", "k"),
		"branches": press(newShowcaseModel(t, root), "enter", "down"),
		"search":   press(newShowcaseModel(t, root), "/", "s", "e", "r", "v", "i", "c", "e"),
	}

	out := filepath.Join("docs", "screenshots")
	os.MkdirAll(out, 0o755)
	for name, m := range shots {
		ansi := filepath.Join(t.TempDir(), name+".ansi")
		// freeze's bundled font has no ⎇ glyph and draws an empty box instead.
		os.WriteFile(ansi, []byte(strings.Replace(m.View(), " ⎇ repotui ", " repotui ", 1)), 0o644)

		cmd := exec.Command("freeze", ansi, "--language", "ansi", "--window",
			"--background", "#171717", "--padding", "20,24", "--border.radius", "8",
			"--font.size", "14", "--output", filepath.Join(out, name+".png"))
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("freeze %s: %v\n%s", name, err, b)
		}
	}
}
