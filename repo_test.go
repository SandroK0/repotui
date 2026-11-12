package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", file)
	run(t, dir, "commit", "-q", "-m", file)
}

func isolateGit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

// setup returns a clone of a local origin on main, plus a second working copy ("upstream") that can push to that origin.
func setup(t *testing.T) (upstream string, repo Repo) {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	run(t, dir, "init", "-q", "--bare", "-b", "main", origin)

	upstream = filepath.Join(dir, "upstream")
	run(t, dir, "clone", "-q", origin, upstream)
	commit(t, upstream, "readme")
	run(t, upstream, "push", "-q", "origin", "main")

	clone := filepath.Join(dir, "clone")
	run(t, dir, "clone", "-q", origin, clone)
	return upstream, Repo{Name: "clone", Path: clone}
}

func status(t *testing.T, r Repo) Status {
	t.Helper()
	s, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStatusOfFreshClone(t *testing.T) {
	_, repo := setup(t)

	s := status(t, repo)

	if s != (Status{Branch: "main", HasUpstream: true}) {
		t.Fatalf("got %+v", s)
	}
}

func TestStatusCountsChangedAndUntrackedFiles(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("edited"), 0o644)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("new"), 0o644)

	s := status(t, repo)
	if s.Changes != 2 || s.Added != 2 || s.Deleted != 1 {
		t.Fatalf("got %+v, want 2 changes, +2 -1", s)
	}
}

func TestStatusReportsAheadAndBehind(t *testing.T) {
	upstream, repo := setup(t)
	commit(t, upstream, "theirs")
	run(t, upstream, "push", "-q")
	commit(t, repo.Path, "mine")
	if err := repo.Fetch(); err != nil {
		t.Fatal(err)
	}

	s := status(t, repo)

	if s.Ahead != 1 || s.Behind != 1 {
		t.Fatalf("ahead %d behind %d, want 1 and 1", s.Ahead, s.Behind)
	}
}

func TestStatusOfBranchWithoutUpstream(t *testing.T) {
	_, repo := setup(t)
	if err := repo.CreateBranch("local-only"); err != nil {
		t.Fatal(err)
	}

	s := status(t, repo)

	if s.Branch != "local-only" || s.HasUpstream {
		t.Fatalf("got %+v", s)
	}
}

func TestBranchesListsLocalThenRemoteOnlyWithoutDuplicates(t *testing.T) {
	upstream, repo := setup(t)
	run(t, upstream, "checkout", "-q", "-b", "feature")
	commit(t, upstream, "feature")
	run(t, upstream, "push", "-q", "origin", "feature")
	repo.Fetch()
	repo.CreateBranch("mine")

	branches, err := repo.Branches()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"main": false, "mine": false, "feature": true}
	if len(branches) != len(want) {
		t.Fatalf("got %+v", branches)
	}
	for i, b := range branches {
		remote, ok := want[b.Name]
		if !ok || remote != b.Remote {
			t.Fatalf("unexpected %+v in %+v", b, branches)
		}
		if b.Remote && i < 2 {
			t.Fatalf("remote branch listed before local ones: %+v", branches)
		}
	}
}

func TestCheckoutOfRemoteOnlyBranchTracksIt(t *testing.T) {
	upstream, repo := setup(t)
	run(t, upstream, "checkout", "-q", "-b", "feature")
	run(t, upstream, "push", "-q", "origin", "feature")
	repo.Fetch()

	if err := repo.Checkout("feature"); err != nil {
		t.Fatal(err)
	}

	if s := status(t, repo); s.Branch != "feature" || !s.HasUpstream {
		t.Fatalf("got %+v", s)
	}
}

func TestCheckoutOfUnknownBranchReturnsGitsMessage(t *testing.T) {
	_, repo := setup(t)

	err := repo.Checkout("nope")

	if err == nil || !strings.Contains(err.Error(), "'nope' did not match") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckoutDefaultUsesOriginsDefaultBranch(t *testing.T) {
	_, repo := setup(t)
	repo.CreateBranch("elsewhere")

	name, err := repo.CheckoutDefault()

	if err != nil || name != "main" || status(t, repo).Branch != "main" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestCheckoutDefaultFallsBackToLocalMaster(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "master")
	commit(t, dir, "readme")
	run(t, dir, "checkout", "-q", "-b", "elsewhere")
	repo := Repo{Name: "solo", Path: dir}

	name, err := repo.CheckoutDefault()

	if err != nil || name != "master" || status(t, repo).Branch != "master" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestStashIncludesUntrackedFiles(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("edited"), 0o644)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("new"), 0o644)

	if err := repo.Stash(); err != nil {
		t.Fatal(err)
	}

	if got := status(t, repo).Changes; got != 0 {
		t.Fatalf("Changes = %d after stash", got)
	}
	if list := run(t, repo.Path, "stash", "list"); !strings.Contains(list, "repotui ") {
		t.Fatalf("stash list = %q", list)
	}
}

func TestPullFastForwards(t *testing.T) {
	upstream, repo := setup(t)
	commit(t, upstream, "theirs")
	run(t, upstream, "push", "-q")

	if err := repo.Pull(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(repo.Path, "theirs")); err != nil {
		t.Fatal("pulled file missing")
	}
	if s := status(t, repo); s.Behind != 0 {
		t.Fatalf("still behind: %+v", s)
	}
}

func TestChangesListsEachKindOfChange(t *testing.T) {
	_, repo := setup(t)
	commit(t, repo.Path, "gone")
	commit(t, repo.Path, "old")
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("edited"), 0o644)
	os.Remove(filepath.Join(repo.Path, "gone"))
	run(t, repo.Path, "mv", "old", "renamed")
	os.WriteFile(filepath.Join(repo.Path, "staged"), []byte("s"), 0o644)
	run(t, repo.Path, "add", "staged")
	os.MkdirAll(filepath.Join(repo.Path, "dir"), 0o755)
	os.WriteFile(filepath.Join(repo.Path, "dir", "untracked"), []byte("u"), 0o644)

	got, err := repo.Changes()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]Change{
		"readme":        {Path: "readme", Code: "M", Added: 1, Deleted: 1},
		"gone":          {Path: "gone", Code: "D", Deleted: 1},
		"renamed":       {Path: "renamed", OldPath: "old", Code: "R"},
		"staged":        {Path: "staged", Code: "A", Added: 1},
		"dir/untracked": {Path: "dir/untracked", Code: "?", Added: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, c := range got {
		if want[c.Path] != c {
			t.Errorf("got %+v, want %+v", c, want[c.Path])
		}
	}
}

func TestChangesCountLinesAcrossStagedAndUnstagedEdits(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("one\ntwo\n"), 0o644)
	run(t, repo.Path, "add", "readme")
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("one\ntwo\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("a\nb\nc\n"), 0o644)
	os.WriteFile(filepath.Join(repo.Path, "blob"), []byte{0, 1, 2}, 0o644)

	got, err := repo.Changes()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]Change{
		"readme": {Path: "readme", Code: "M", Added: 3, Deleted: 1},
		"new":    {Path: "new", Code: "?", Added: 3},
		"blob":   {Path: "blob", Code: "?", Binary: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, c := range got {
		if want[c.Path] != c {
			t.Errorf("got %+v, want %+v", c, want[c.Path])
		}
	}
}

func TestChangesOfCleanRepoIsEmpty(t *testing.T) {
	_, repo := setup(t)

	got, err := repo.Changes()

	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDiffCombinesStagedAndUnstagedEdits(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("one\n"), 0o644)
	run(t, repo.Path, "add", "readme")
	os.WriteFile(filepath.Join(repo.Path, "readme"), []byte("one\ntwo\n"), 0o644)

	lines, err := repo.Diff(Change{Path: "readme", Code: "M"})
	if err != nil {
		t.Fatal(err)
	}

	text := strings.Join(lines, "\n")
	for _, want := range []string{"-readme", "+one", "+two", "@@"} {
		if !strings.Contains(text, want) {
			t.Errorf("diff missing %q:\n%s", want, text)
		}
	}
}

func TestDiffShowsUntrackedFileAsAdded(t *testing.T) {
	_, repo := setup(t)
	os.WriteFile(filepath.Join(repo.Path, "new"), []byte("a\nb\n"), 0o644)

	lines, err := repo.Diff(Change{Path: "new", Code: "?"})

	if err != nil || strings.Join(lines, "\n") != "@@ -0,0 +1,2 @@\n+a\n+b" {
		t.Fatalf("got %q, %v", lines, err)
	}
}

func TestDiffWorksBeforeTheFirstCommit(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a"), []byte("hello\n"), 0o644)
	run(t, dir, "add", "a")
	repo := Repo{Name: "fresh", Path: dir}

	lines, err := repo.Diff(Change{Path: "a", Code: "A"})

	if err != nil || !strings.Contains(strings.Join(lines, "\n"), "+hello") {
		t.Fatalf("got %q, %v", lines, err)
	}
}
