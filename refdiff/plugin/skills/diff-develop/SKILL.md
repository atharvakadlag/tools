---
name: diff-develop
description: Start a background refdiff server that shows the live diff between origin/develop (or another base ref) and the current branch's working tree in the browser. Use when the user asks to see/review/open the diff against develop, invokes /diff-develop, or asks to stop that diff server.
---

# diff-develop

Opens a live, auto-refreshing browser diff of `origin/develop` → current working tree (committed + uncommitted changes) using the `refdiff` tool.

## Start

Run from the repo the user is working in:

```bash
"${CLAUDE_PLUGIN_ROOT}/skills/diff-develop/start.sh"            # base: origin/develop
"${CLAUDE_PLUGIN_ROOT}/skills/diff-develop/start.sh" origin/main # any other base ref
```

The script returns immediately: the server keeps running in the background (nohup), the browser opens, and it prints the URL. Each repo gets a stable port, so re-running restarts the server on the same URL. Report the URL to the user.

On first use, if `refdiff` is not on `PATH`, the script downloads the prebuilt binary from GitHub Releases into `~/.cache/refdiff/` (macOS arm64 only). On other platforms it tells the user how to build from source.

If it fails with `<ref> not found`, ask whether to run `git fetch origin <branch>`, then retry.

## Stop

```bash
"${CLAUDE_PLUGIN_ROOT}/skills/diff-develop/start.sh" --stop
```

## Notes

- The page updates live as files are edited, committed, or staged. It does not pick up new commits on the base ref until a `git fetch`; after a fetch it refreshes on its own.
- Untracked files are not shown (same as `git diff`).
- Server log: `$TMPDIR/refdiff-<port>.log`.
