package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const configName = ".repotui.conf"

// LoadRepos returns the git repos directly inside root plus the extra repos listed in root's .repotui.conf, sorted by name.
//
// Each config line is a path, optionally named: "~/code/sdk" or "sdk = ~/code/sdk".
// Blank lines and lines starting with # are ignored; relative paths are relative to root.
func LoadRepos(root string) ([]Repo, error) {
	repos := FindRepos(root)

	data, err := os.ReadFile(filepath.Join(root, configName))
	if errors.Is(err, fs.ErrNotExist) {
		return repos, nil
	}
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	for _, r := range repos {
		seen[r.Path] = true
	}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, path, named := strings.Cut(line, "=")
		if !named {
			name, path = "", name
		}
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if path == "" {
			return nil, fmt.Errorf("%s line %d: missing path", configName, n+1)
		}
		path = expandPath(root, path)
		if name == "" {
			name = filepath.Base(path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		repos = append(repos, Repo{Name: name, Path: path})
	}

	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, nil
}

func expandPath(root, path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = filepath.Join(os.Getenv("HOME"), path[1:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path)
}
