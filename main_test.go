package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		m = drive(m, cmd)
	}
	return m
}

func newModel(t *testing.T, repos ...Repo) model {
	t.Helper()
	var views []repoView
	for _, r := range repos {
		views = append(views, repoView{repo: r})
	}
	var m tea.Model = model{root: t.TempDir(), repos: views}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return drive(m, m.(model).refresh()).(model)
}

func TestDiffKeyOnCleanRepoOnlyLogs(t *testing.T) {
	_, repo := setup(t)

	m := press(newModel(t, repo), "d").(model)

	if m.mode != modeList || len(m.log) != 1 || m.log[0].text != "no changes" {
		t.Fatalf("mode %v, log %+v", m.mode, m.log)
	}
}

func TestDiffKeysWalkFilesThenDiffAndBack(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("edited\n"), 0o644)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("hello\n"), 0o644)
	var m tea.Model = newModel(t, repo)

	m = press(m, "d")
	if got := m.(model); got.mode != modeDiffFiles || len(got.files) != 2 {
		t.Fatalf("after d: mode %v, files %+v", got.mode, got.files)
	}

	m = press(m, "enter")
	if got := m.(model); got.mode != modeDiff || !strings.Contains(m.View(), "+edited") {
		t.Fatalf("after enter: mode %v\n%s", got.mode, m.View())
	}

	m = press(m, "n")
	if got := m.(model); got.fcursor != 1 || !strings.Contains(m.View(), "+hello") {
		t.Fatalf("after n: cursor %d\n%s", got.fcursor, m.View())
	}

	m = press(m, "esc")
	if m.(model).mode != modeDiffFiles {
		t.Fatalf("after esc: mode %v", m.(model).mode)
	}
	m = press(m, "esc")
	if m.(model).mode != modeList {
		t.Fatalf("after second esc: mode %v", m.(model).mode)
	}
}

func TestRefreshKeepsDiffScrollPosition(t *testing.T) {
	_, repo := setup(t)
	var lines []string
	for i := range 100 {
		lines = append(lines, strings.Repeat("x", i%7+1))
	}
	os.WriteFile(filepath.Join(repo.Path, "long"), []byte(strings.Join(lines, "\n")), 0o644)
	m := press(newModel(t, repo), "d", "enter", "j", "j", "j")

	m = drive(m, m.(model).refresh())

	if got := m.(model); got.mode != modeDiff || got.dscroll != 3 {
		t.Fatalf("mode %v, scroll %d", got.mode, got.dscroll)
	}
}

func TestLateDiffIsIgnoredAfterLeaving(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("hello\n"), 0o644)
	m := press(newModel(t, repo), "d").(model)
	cmd := loadDiff(0, 0, repo, m.files[0])
	m = press(m, "esc").(model)

	got, _ := m.Update(cmd())

	if got.(model).mode != modeList {
		t.Fatalf("mode %v", got.(model).mode)
	}
}

func TestSideBySidePairsRemovedWithAddedLines(t *testing.T) {
	rows := sideBySide([]string{
		"@@ -10,4 +10,5 @@ func x",
		" keep",
		"-old a",
		"-old b",
		"+new a",
		"+new b",
		"+new c",
		"\\ No newline at end of file",
		" tail",
	})

	want := []sideRow{
		{kind: rowHunk, left: "@@ -10,4 +10,5 @@ func x", src: 0},
		{kind: rowContext, left: "keep", right: "keep", lnum: 10, rnum: 10, src: 1},
		{kind: rowChange, left: "old a", right: "new a", lnum: 11, rnum: 11, src: 2},
		{kind: rowChange, left: "old b", right: "new b", lnum: 12, rnum: 12, src: 3},
		{kind: rowChange, right: "new c", rnum: 13, src: 6},
		{kind: rowContext, left: "tail", right: "tail", lnum: 13, rnum: 14, src: 8},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestSplitToggleKeepsPlaceAndShowsBothSides(t *testing.T) {
	_, repo := setup(t)
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	run(t, repo.Path, "rm", "-q", "readme")
	os.WriteFile(filepath.Join(repo.Path, "long"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	run(t, repo.Path, "add", "long")
	run(t, repo.Path, "commit", "-q", "-m", "long")
	for i := range lines {
		lines[i] = fmt.Sprintf("changed %d", i+1)
	}
	os.WriteFile(filepath.Join(repo.Path, "long"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	m := press(newModel(t, repo), "d", "enter")

	m = press(m, "s")
	view := m.View()
	if !m.(model).split || !strings.Contains(view, "line 1 ") || !strings.Contains(view, "changed 1 ") || !strings.Contains(view, "│") {
		t.Fatalf("split view:\n%s", view)
	}

	m = press(m, "j", "j", "s")
	if got := m.(model); got.split || got.dscroll != 2 {
		t.Fatalf("back to unified: split %v, scroll %d", got.split, got.dscroll)
	}
}
