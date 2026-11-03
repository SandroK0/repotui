# repotui

A terminal UI for keeping an eye on a folder full of git repos. See every repo's current branch, whether it's ahead or behind, and whether it has uncommitted changes, all on one screen. Switch branches, pull, fetch and stash without `cd`-ing around.

![repotui overview](docs/screenshots/overview.png)

## Features

- **One view of every repo** in a folder: branch, ahead/behind its upstream, number of changed files.
- **Live**: re-reads every repo every 3 seconds, so changes made in your editor or another terminal show up on their own.
- **Switch branches** from a filterable picker that includes branches that only exist on the remote.
- **Pull, fetch, stash and jump to master/main** for one repo or all of them at once.
- **Search** repos by name or branch, vi-style, and run the "all" actions on just the matches.
- **Activity panel** with the result of each action, including git's own error message when something fails.

## Install

Requires Go 1.27+ and git.

```bash
go install github.com/SandroK0/repotui@latest
```

Or build from source:

```bash
git clone https://github.com/SandroK0/repotui.git
cd repotui
go build -o repotui .
```

## Usage

```bash
repotui ~/code
```

Pass the folder that contains your repos. repotui looks at its direct subfolders and lists every one that is a git repo. With no argument it uses `~/redberry/plannr`.

### Keys

| Key | Action |
| --- | --- |
| `↑` `↓` / `j` `k` | Move between repos |
| `/` | Search repos by name or branch |
| `esc` | Clear the search |
| `enter` / `b` | Open the branch picker for the selected repo |
| `p` / `P` | Pull the selected repo / all repos |
| `f` / `F` | Fetch the selected repo / all repos |
| `m` / `M` | Switch to master or main in the selected repo / all repos |
| `s` / `S` | Stash changes in the selected repo / all repos with changes |
| `n` | Create a new branch in the selected repo |
| `r` | Refresh now |
| `q` | Quit |

While a search is active, the "all" keys (`P`, `F`, `M`, `S`) only act on the repos that match.

### Switching branches

![Branch picker](docs/screenshots/branches.png)

Type to filter, `↑` `↓` to move, `enter` to check out. Branches that only exist on the remote are tagged `remote`; checking one out creates a local branch that tracks it.

### Searching

![Searching repos](docs/screenshots/search.png)

## How it behaves

- **Pull** only fast-forwards (`git pull --ff-only`). If your branch has diverged, nothing is merged and the error shows in the activity panel.
- **Fetch** prunes branches that were deleted on the remote.
- **Stash** includes untracked files and labels each stash `repotui <date time>`, so you can find it in `git stash list`.
- **master/main** uses the remote's default branch, falling back to a local `master` or `main`.
- Any git command that takes longer than 60 seconds is stopped and reported as timed out.
- git never prompts for credentials; a command that needs them fails instead of hanging.

## Development

```bash
go test ./...
```

The tests run against throwaway git repos with a local remote, so they need no network and ignore your own git config.

The screenshots above are generated from a folder of fake repos. To regenerate them, install [freeze](https://github.com/charmbracelet/freeze) and run:

```bash
go test -tags screenshots -run TestScreenshots .
```
