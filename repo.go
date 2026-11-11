package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const gitTimeout = 60 * time.Second

type Repo struct {
	Name string
	Path string
}

type Status struct {
	Branch        string
	Ahead, Behind int
	HasUpstream   bool
	Changes       int
}

type Branch struct {
	Name   string
	Remote bool
}

// Change is one file that differs from HEAD. Code is M, A, D, R or ? (untracked); OldPath is set for renames.
type Change struct {
	Path    string
	OldPath string
	Code    string
}

// emptyTree is git's well-known hash of an empty tree, used as the base when a repo has no commits yet.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func FindRepos(root string) []Repo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var repos []Repo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			repos = append(repos, Repo{Name: e.Name(), Path: p})
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

func (r Repo) Status() (Status, error) {
	// Without these checks a path inside another repo would silently report that repo's status.
	if _, err := os.Stat(r.Path); err != nil {
		return Status{}, errors.New("folder not found")
	}
	if _, err := os.Stat(filepath.Join(r.Path, ".git")); err != nil {
		return Status{}, errors.New("not a git repo")
	}
	out, err := r.git("status", "--porcelain=v2", "--branch")
	if err != nil {
		return Status{}, err
	}
	var s Status
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			s.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			s.HasUpstream = true
		case strings.HasPrefix(line, "# branch.ab "):
			fmt.Sscanf(strings.TrimPrefix(line, "# branch.ab "), "+%d -%d", &s.Ahead, &s.Behind)
		case line != "" && !strings.HasPrefix(line, "#"):
			s.Changes++
		}
	}
	return s, nil
}

// Branches lists local branches, then branches that only exist on a remote, each group most recently committed first.
func (r Repo) Branches() ([]Branch, error) {
	out, err := r.git("for-each-ref", "--sort=-committerdate", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	refs := strings.Split(out, "\n")
	seen := map[string]bool{}
	var local, remote []Branch
	for _, ref := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			seen[name] = true
			local = append(local, Branch{Name: name})
		}
	}
	for _, ref := range refs {
		rest, ok := strings.CutPrefix(ref, "refs/remotes/")
		if !ok {
			continue
		}
		_, name, ok := strings.Cut(rest, "/")
		if !ok || name == "HEAD" || seen[name] {
			continue
		}
		seen[name] = true
		remote = append(remote, Branch{Name: name, Remote: true})
	}
	return append(local, remote...), nil
}

// Checkout switches to a branch, creating a tracking branch when it only exists on a remote.
func (r Repo) Checkout(name string) error {
	_, err := r.git("checkout", name)
	return err
}

func (r Repo) CreateBranch(name string) error {
	_, err := r.git("checkout", "-b", name)
	return err
}

// CheckoutDefault switches to origin's default branch, falling back to a local master or main, and returns the branch it chose.
func (r Repo) CheckoutDefault() (string, error) {
	name := ""
	if out, err := r.git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		name = strings.TrimPrefix(out, "origin/")
	} else {
		for _, b := range []string{"master", "main"} {
			if _, err := r.git("rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
				name = b
				break
			}
		}
	}
	if name == "" {
		return "", errors.New("no default branch found")
	}
	return name, r.Checkout(name)
}

func (r Repo) Pull() error {
	_, err := r.git("pull", "--ff-only")
	return err
}

func (r Repo) Fetch() error {
	_, err := r.git("fetch", "--prune")
	return err
}

func (r Repo) Stash() error {
	_, err := r.git("stash", "push", "--include-untracked", "-m", "repotui "+time.Now().Format("2006-01-02 15:04"))
	return err
}

// Changes lists every file that differs from HEAD, staged or not, including each untracked file.
func (r Repo) Changes() ([]Change, error) {
	out, err := r.gitRaw("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var list []Change
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		x, y, path := f[0], f[1], f[3:]
		c := Change{Path: path}
		switch {
		case x == '?':
			c.Code = "?"
		case x == 'R' || y == 'R':
			c.Code = "R"
			if i+1 < len(fields) {
				i++
				c.OldPath = fields[i]
			}
		case x == 'A':
			c.Code = "A"
		case x == 'D' || y == 'D':
			c.Code = "D"
		default:
			c.Code = "M"
		}
		list = append(list, c)
	}
	return list, nil
}

// Diff returns the unified diff of one change against HEAD, covering staged and unstaged edits together.
// An untracked file is shown as entirely added.
func (r Repo) Diff(c Change) ([]string, error) {
	if c.Code == "?" {
		raw, err := os.ReadFile(filepath.Join(r.Path, c.Path))
		if err != nil {
			return nil, err
		}
		if strings.ContainsRune(string(raw), 0) {
			return []string{"Binary file " + c.Path + " is untracked"}, nil
		}
		text := strings.TrimSuffix(string(raw), "\n")
		if text == "" {
			return []string{"Empty file " + c.Path + " is untracked"}, nil
		}
		lines := strings.Split(text, "\n")
		out := []string{fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines))}
		for _, l := range lines {
			out = append(out, "+"+l)
		}
		return out, nil
	}

	base := "HEAD"
	if _, err := r.git("rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		base = emptyTree
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "-M", base, "--"}
	if c.OldPath != "" {
		args = append(args, c.OldPath)
	}
	out, err := r.gitRaw(append(args, c.Path)...)
	if err != nil {
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{"No differences"}, nil
	}
	return strings.Split(out, "\n"), nil
}

func (r Repo) git(args ...string) (string, error) {
	out, err := r.gitRaw(args...)
	return strings.TrimSpace(out), err
}

// gitRaw runs git and returns its output untouched, for formats where leading spaces matter.
func (r Repo) gitRaw(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	raw, err := cmd.CombinedOutput()
	out := string(raw)

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return out, fmt.Errorf("git %s timed out after %s", args[0], gitTimeout)
	case err != nil && out != "":
		return out, errors.New(lastLine(out))
	}
	return out, err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
