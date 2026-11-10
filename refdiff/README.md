# refdiff

Live, browser-based diff viewer for git refs: syntax highlighting, themes, line references,
fetch & rebase, and tree-sitter go-to-definition / find-references.

```
refdiff                    working tree vs HEAD (live)
refdiff origin/develop     working tree vs origin/develop
refdiff A B                A vs B   (A..B / A...B also work)
refdiff --staged           index vs HEAD
refdiff --no-untracked     hide untracked files (shown by default for working-tree diffs)
refdiff --coverage cover.out   overlay this coverage report (default: found in the repo root)
refdiff comments           print open review notes as a prompt (--json, --resolve <id>, --all, --clear)
refdiff comments --add src/x.go:10-12 -- 'note'   add a note from the command line (what agents use)
refdiff --help
```

## Review workflow

- **Notes**: click or drag line numbers, then press `m` (or "Add note"). Notes are stored in `.git/refdiff/comments.json`,
  quote the code they're about, and re-anchor when the file changes. Review (`r`) lists them; "Copy for Claude"
  copies them as a markdown prompt. In Claude Code, `refdiff comments` prints the same prompt, and
  `refdiff comments --resolve <id>` marks one done; the open page updates live.
- **Commits**: the sidebar lists the commits in the compared range. Click one to see only that commit, shift-click
  to see a range, or pick "Uncommitted changes". Use `[` and `]` to step older or newer.
- **Symbols** (`o`): a change map built with tree-sitter (Go, Python, JS/TS, Java, Rust, Ruby, C/C++, C#, Scala). It lists
  every function, method, type and class the diff adds, removes, renames (detected by body) or modifies. Signature
  changes are flagged, with how many places still call them. A removed or renamed symbol that is still referenced gets
  a ⚠, since that's a likely broken caller. Click a symbol to jump to it, or its ref count to list the call sites.
- **Coverage overlay**: run your tests with coverage (`go test -coverprofile=cover.out`, `lcov.info`, `coverage json`,
  `coverage.xml`, or pass `--coverage <file>`). Added lines get a green or red gutter, untested ones are striped, and each
  file shows its % covered. The sidebar shows "New code covered: 72% · 34 untested lines ›", which steps through them. It updates when you rerun the tests.
- **Search** (`f`): searches every line of the diff, including files not rendered yet, with regex, case and ±-only options. `↵`/`⇧↵` step.
- **Change navigation**: `n`/`p` jump to the next/previous change block across all files.
- **Moved code**: blocks of 3+ lines removed in one place and added in another are coloured purple, with "moved from/to file:line" links.
- **Less noise**: `W` toggles ignoring whitespace. Lockfiles, generated and vendored files (by name or `.gitattributes`
  `linguist-generated`) are collapsed and tagged. The filter supports `api -test *.go is:new is:unviewed is:changed has:note`.
- **Minimap**: a strip on the right edge maps the whole diff, showing changes, moved code, notes, search hits and untested lines. Click or drag to scroll.
- **Claude in the loop** (needs the `claude` CLI). From "✦ Claude":
  - *Review this diff* runs Claude Code headless in the repo. It can read files and run `git diff/log/show`, and it
    posts notes with `refdiff comments --add`, so they appear on the diff as it finds issues. Each note has a severity.
  - *Fix notes* (all open notes, Claude's own, or "✦ Fix with Claude" on a single note) lets it edit the working
    tree. It may only Read, Edit and Write; `rm`, `mv` and shell commands are denied. It resolves each note as it goes, or
    replies when it disagrees. Pass `--agent-allow "Bash(go test:*)"` so it can run your tests.
  - A dock streams its tool calls, cost and summary; "View Claude's changes" diffs the pre-run snapshot against now.
- **Timeline**: whenever the working tree changes, refdiff snapshots it as a git tree, using a private index so your
  real index is untouched. The snapshots are listed in `.git/refdiff/timeline.json`. Click one to see everything that changed since then (live),
  or shift-click two to compare them. Files you marked viewed and that changed afterwards get a
  "changed since viewed" button that shows only the new edits.
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
