# tools

## Claude Code plugins

```
/plugin marketplace add atharvakadlag/tools
/plugin install refdiff@atharva-tools
/plugin install mmdview@atharva-tools
```

- **refdiff**: live browser diff of origin/develop (or any ref) against your working tree. Skill: `diff-develop`.
- **mmdview**: live-reloading Mermaid viewer that Claude draws into and checks for render errors. Skill: `mermaid`.

Already added the marketplace? Run `/plugin marketplace update atharva-tools` to pick up new plugins.

## Scripts

- **mr-watch**: keeps a GitLab MR rebased on its target, plays manual test-stage jobs, and exits (with a macOS notification) when unit tests pass, fail, or the MR hits conflicts. Needs `glab`. Install: `ln -s "$PWD/mr-watch/mr-watch" ~/.local/bin/mr-watch`, then `mr-watch <iid|mr-url>`.
- **cc-snippets**: iTerm2 Toolbelt panel with Copy buttons (and ⌃⌥1–9 shortcuts) for the code blocks in Claude Code's latest reply in each tab. See `cc-snippets/README.md`.

## Other

- **[statusline](statusline/)**: two-row powerline status line for Claude Code with git, context and usage-pace segments.
