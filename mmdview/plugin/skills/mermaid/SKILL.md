---
name: mermaid
description: Show Mermaid diagrams to the user in a live-reloading browser viewer (mmdview), and get back whether they rendered. Use when a diagram would explain something better than prose — architecture, request/data flow, sequence, state machine, ER schema, class hierarchy, timeline, git graph — when the user asks to visualize, diagram, chart or draw something, when they ask to preview mermaid in a file, or invoke /mermaid.
---

# mermaid

`mmdview` serves `.mmd` / `.mermaid` files and ```` ```mermaid ```` blocks in markdown, renders them in the browser, and re-renders on every save. The page reports parse errors back to the server, so you can confirm a diagram works before telling the user it does.

Script: `"${CLAUDE_PLUGIN_ROOT}/skills/mermaid/mmd.sh"` (below: `mmd.sh`).

## Workflow

1. Pick where the diagram lives:
   - **Scratch / explanatory diagrams** (the default): write to the diagrams dir from `mmd.sh dir`, e.g. `<dir>/auth-flow.mmd`. Use a short descriptive kebab-case name; reuse the same file when revising the same diagram, a new file for a new one.
   - **Diagrams that belong in the repo** (docs, READMEs, ADRs): edit the repo file; markdown files with several ```` ```mermaid ```` blocks are fine.
2. Write the file with the Write/Edit tool.
3. Run `mmd.sh show <file>`. It starts the server if needed, opens or focuses the browser tab on that file, waits for the render and prints JSON, for example:

   ```json
   {"path":"auth-flow.mmd","ok":true,"rendered":true,"viewers":1,"diagrams":[{"index":0,"line":1,"ok":true}]}
   ```

4. If `ok` is false, read each failing diagram's `error` (and `error_line`, a line number in the file), fix the file, and run `mmd.sh check <file>`. Repeat until `ok` is true. Don't tell the user a diagram is ready while it's failing.
5. Give the user the URL and one line about what the diagram shows. Don't paste the Mermaid source into the chat unless they ask for it.

Revising: edit the same file. The open tab re-renders on its own, keeps the zoom level, and follows whichever file changed most recently. Run `mmd.sh check <file>` after each edit to confirm it still parses.

`rendered: false` means no open page rendered this revision (`hint` says why, usually "no browser has the viewer open"). Run `mmd.sh show <file>` to open one. In that case the syntax hasn't been checked, so say so instead of claiming it works.

## Other commands

```bash
mmd.sh start [dir]   # serve a directory (default: the diagrams dir) and open the browser
mmd.sh stop [dir]    # stop that server
```

Each served directory gets its own stable port, so URLs stay the same across restarts. Server log: `$TMPDIR/mmdview-<port>.log`. If `mmdview` isn't on PATH, the script downloads the release binary into `~/.cache/mmdview/` on first use (macOS and Linux, arm64/amd64).

## Writing diagrams that render

- The first line is the diagram type: `flowchart LR`, `sequenceDiagram`, `stateDiagram-v2`, `erDiagram`, `classDiagram`, `gantt`, `timeline`, `mindmap`, `gitGraph`, `journey`, `pie`, `quadrantChart`, `xychart-beta`, `block-beta`, `architecture-beta`.
- Quote any label with punctuation: `A["POST /v1/users (json)"]`, `A -->|"200 OK"| B`. Parentheses, brackets, `:`, `;`, `#`, `/` and `"` in unquoted labels are the most common cause of parse errors. Inside a quoted label, write `#quot;` for a literal `"`.
- Node IDs are plain identifiers (`apiGw`, `db1`), not reserved words. `end` breaks flowcharts; use `End` or `done`.
- Use `<br/>` for line breaks inside labels, not `\n`.
- Group with `subgraph id["Title"] ... end`. Keep one direction (`LR` for pipelines, `TD` for hierarchies).
- Sequence diagrams: `participant A as Alias`, `->>` for calls, `-->>` for replies, `activate`/`deactivate`, `alt`/`else`/`end`, `Note over A,B: text`.
- Aim for under about 30 nodes per diagram. Split big systems into several diagrams (or several blocks in one markdown file) rather than one giant one.
- Optional title via front matter:

  ```
  ---
  title: Checkout flow
  ---
  flowchart LR
    ...
  ```

- Don't hard-code colours or `%%{init: {theme}}%%`. The viewer picks a light or dark theme to match the page.
