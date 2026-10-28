# cc-snippets

Copy buttons and keyboard shortcuts for the code blocks in Claude Code's latest reply, per iTerm2 tab, shown in an iTerm2 Toolbelt panel.

- `extract_snippets.py`: Claude Code `Stop` hook. Writes the last reply's code blocks to `~/.claude/cc-snippets/<iTerm2 session id>.json` (left alone if the reply had none).
- `cc_snippets.py`: iTerm2 AutoLaunch script. Serves `panel.html` on `127.0.0.1:47911` as the "Claude Snippets" Toolbelt tool and registers `copy_snippet(n)`. Both follow the focused tab/pane.

## Install

1. Hook, in `~/.claude/settings.json`:
   ```json
   "hooks": { "Stop": [ { "hooks": [ { "type": "command", "command": "python3 /path/to/cc-snippets/extract_snippets.py" } ] } ] }
   ```
2. `ln -s "$PWD/cc-snippets/cc_snippets.py" ~/Library/Application\ Support/iTerm2/Scripts/AutoLaunch/`
3. iTerm2 → Settings → General → Magic → enable **Python API**. Then Scripts → Manage → **Install Python Runtime**, and restart iTerm2.
4. View → Show Toolbelt, then Toolbelt → **Claude Snippets**.

## Shortcuts

Settings → Keys → Key Bindings → `+` → Action **Invoke Script Function**:

| Keys | Function |
|---|---|
| ⌃⌥1 … ⌃⌥9 | `copy_snippet(n: 1)` … `copy_snippet(n: 9)` |
| ⌃⌥C | `copy_snippet(n: 0)` (last snippet) |
