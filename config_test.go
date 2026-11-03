package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, configName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReposAddsReposListedInConfig(t *testing.T) {
	isolateGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "work")
	run(t, home, "init", "-q", filepath.Join(root, "api"))
	run(t, home, "init", "-q", filepath.Join(home, "elsewhere", "plannr-sdk"))
	run(t, home, "init", "-q", filepath.Join(home, "tools", "cli"))
	writeConfig(t, root, `
# repos that live outside this folder
sdk = ~/elsewhere/plannr-sdk
../tools/cli

./api
`)

	repos, err := LoadRepos(root)
	if err != nil {
		t.Fatal(err)
	}

	want := []Repo{
		{Name: "api", Path: filepath.Join(root, "api")},
		{Name: "cli", Path: filepath.Join(home, "tools", "cli")},
		{Name: "sdk", Path: filepath.Join(home, "elsewhere", "plannr-sdk")},
	}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("got  %+v\nwant %+v", repos, want)
	}
}

func TestLoadReposWithoutConfigOnlyScansTheFolder(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "api")

	repos, err := LoadRepos(root)

	if err != nil || len(repos) != 1 || repos[0].Name != "api" {
		t.Fatalf("repos %+v err %v", repos, err)
	}
}

func TestLoadReposRejectsEntryWithoutPath(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "# comment\nsdk =\n")

	_, err := LoadRepos(root)

	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusOfMissingFolder(t *testing.T) {
	_, err := Repo{Path: filepath.Join(t.TempDir(), "gone")}.Status()

	if err == nil || err.Error() != "folder not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusOfFolderInsideAnotherRepoIsNotARepo(t *testing.T) {
	_, repo := setup(t)
	inner := filepath.Join(repo.Path, "plain")
	os.Mkdir(inner, 0o755)

	_, err := Repo{Path: inner}.Status()

	if err == nil || err.Error() != "not a git repo" {
		t.Fatalf("err = %v", err)
	}
}
