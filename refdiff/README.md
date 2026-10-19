# refdiff

Live, browser-based diff viewer for git refs: syntax highlighting, themes, line references,
fetch & rebase, and tree-sitter go-to-definition / find-references.

```
refdiff                    working tree vs HEAD (live)
refdiff origin/develop     working tree vs origin/develop
refdiff A B                A vs B   (A..B / A...B also work)
refdiff --staged           index vs HEAD
refdiff --help
```

## Install

Prebuilt binaries are published as GitHub Releases (tags `refdiff-v*`):

```
gh release download -R atharvakadlag/tools --pattern 'refdiff-*-darwin-arm64.tar.gz' --dir /tmp
tar -xzf /tmp/refdiff-*-darwin-arm64.tar.gz -C ~/.local/bin
```

## Build

```
make            # build ./refdiff (needs Go + Xcode Command Line Tools for cgo)
make test
make release V=0.2.0   # tag + publish a GitHub Release (commit and push first)
```
