# refdiff

Live, browser-based diff viewer for git refs: syntax highlighting, themes, line references,
fetch & rebase, and tree-sitter go-to-definition / find-references.

```
refdiff                    working tree vs HEAD (live)
refdiff origin/develop     working tree vs origin/develop
refdiff A B                A vs B   (A..B / A...B also work)
refdiff --staged           index vs HEAD
refdiff --no-untracked     hide untracked files (shown by default for working-tree diffs)
refdiff comments           print open review notes as a prompt (--json, --resolve <id>, --all, --clear)
refdiff --help
```

## Review workflow

- **Notes**: click or drag line numbers, then press `m` (or "Add note"). Notes are stored in `.git/refdiff/comments.json`,
  quote the code they're about, and re-anchor when the file changes. Review (`r`) lists them; "Copy for Claude"
  copies them as a markdown prompt. In Claude Code, `refdiff comments` prints the same prompt, and
  `refdiff comments --resolve <id>` marks one done; the open page updates live.
- **Commits**: the sidebar lists the commits in the compared range. Click one to see only that commit, shift-click
  to see a range, or pick "Uncommitted changes". Use `[` and `]` to step older or newer.
- **Palette**: `⌘K` or `t` jumps to files, commits and notes, and runs actions such as views, themes and untracked.
- Untracked files (badge `U`), word-level change highlighting, file tree with viewed progress.

## Install

Prebuilt binaries are published as GitHub Releases (tags `refdiff-v*`):

```
gh release download -R atharvakadlag/tools --pattern 'refdiff-*-darwin-arm64.tar.gz' --dir /tmp
tar -xzf /tmp/refdiff-*-darwin-arm64.tar.gz -C ~/.local/bin
```

## Claude Code plugin

The `diff-develop` skill (`plugin/skills/diff-develop`) starts a background refdiff server against `origin/develop`
(or another base) for the current repo. On first use it downloads the release binary unless `refdiff` is on PATH.

```
/plugin marketplace add atharvakadlag/tools
/plugin install refdiff@atharva-tools
```

## Build

```
make            # build ./refdiff (needs Go + Xcode Command Line Tools for cgo)
make test
make release V=0.2.0   # tag + publish a GitHub Release (commit and push first)
```
