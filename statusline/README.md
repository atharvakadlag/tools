# statusline

Two-row powerline status line for Claude Code, in Catppuccin Mocha.

```
 ⚡ Opus 5.5  ~/code/project   main ~9 ?8
  ▰▰▱▱▱ 34%   5h ▰▰▰▱▱ 60%  2h59m   1w ▰▰▱▱▱ 30%  3d11h   45m +120 -31 $1.23
```

- Row 1: model, directory (`~` for home, `…/last/two` when deep), git branch with ahead/behind (↑/↓),
  staged `+`, modified `~`, untracked `?`. The git segment turns peach when the tree is dirty.
- Row 2: context window, 5-hour and weekly usage with time until reset, session time, lines changed and cost.
- Context goes green → yellow → red past 20% and 50%. Usage is judged against a linear pace through its window:
  green on pace, yellow up to 10 points ahead, red beyond (60% used 2h into a 5h window is red, since 2h
  "allows" only 40%).
- Segments with no data are left out, and so is a row with nothing in it.

Needs a [Nerd Font](https://www.nerdfonts.com/), a truecolor terminal, bash 4.2+ and `jq`.

## Install

```
curl -fsSL https://raw.githubusercontent.com/atharvakadlag/tools/main/statusline/statusline.sh -o ~/.claude/statusline.sh
chmod +x ~/.claude/statusline.sh
```

Then in `~/.claude/settings.json`:

```json
"statusLine": {
  "type": "command",
  "command": "~/.claude/statusline.sh",
  "padding": 0,
  "refreshInterval": 30
}
```

`refreshInterval` keeps the reset countdowns moving while the session is idle. Colours are hex values at the top
of the script.
