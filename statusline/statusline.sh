#!/usr/bin/env bash
# Claude Code powerline status line (needs a Nerd Font + truecolor terminal; bash 4.2+)
#
#   model  dir  git
#   ctx  5h  1w  session
#
# Context and usage segments change colour: green → yellow → red.
# Usage is judged against a linear pace through its window
# (60% used 2h into a 5h window is ahead of pace, since 2h "allows" only 40%).

input=$(cat)
now=$(date +%s)

# ── Palette (Catppuccin Mocha) ────────────────────────────────────────────────
CRUST=11111b; BASE=1e1e2e; SURFACE0=313244; SURFACE1=45475a; OVERLAY0=6c7086
TEXT=cdd6f4; SUBTEXT=a6adc8
MAUVE=cba6f7; BLUE=89b4fa; LAVENDER=b4befe; TEAL=94e2d5
GREEN=a6e3a1; YELLOW=f9e2af; PEACH=fab387; RED=f38ba8

hex2rgb() { printf '%d;%d;%d' 0x${1:0:2} 0x${1:2:2} 0x${1:4:2}; }
fg() { printf '\033[38;2;%sm' "$(hex2rgb "$1")"; }
bg() { printf '\033[48;2;%sm' "$(hex2rgb "$1")"; }
RESET=$'\033[0m'; BOLD=$'\033[1m'; NOBOLD=$'\033[22m'

# ── Glyphs ────────────────────────────────────────────────────────────────────
CAP_L=$''; ARROW=$''; CAP_R=$''; THIN=$''
I_MODEL=$''; I_DIR=$''; I_HOME=$''; I_BRANCH=$''
I_CTX=$''; I_5H=$''; I_1W=$''; I_CLOCK=$''
I_UP=$''; I_DOWN=$''

# ── Powerline segment builder ─────────────────────────────────────────────────
line=""; prev_bg=""
# seg <bg> <fg> <text>
seg() {
  if [ -z "$prev_bg" ]; then
    line+="$(fg "$1")$CAP_L"
  else
    line+="$(fg "$prev_bg")$(bg "$1")$ARROW"
  fi
  line+="$(bg "$1")$(fg "$2") $3 "
  prev_bg=$1
}
# end_row: close the current row's last segment and start a new row
end_row() {
  [ -z "$prev_bg" ] && return
  line+="$RESET$(fg "$prev_bg")$ARROW$RESET"$'\n'
  prev_bg=""
}

# ── Helpers ───────────────────────────────────────────────────────────────────
# bar <pct> -> 5-cell ▰▱ meter
bar() {
  local n=$(awk -v p="$1" 'BEGIN{n=int((p+10)/20); if(n>5)n=5; if(n<0)n=0; print n}') out="" i
  for ((i=0; i<5; i++)); do (( i < n )) && out+="▰" || out+="▱"; done
  printf '%s' "$out"
}
# dur <seconds> -> 4d3h / 2h41m / 12m
dur() {
  local s=$1; (( s < 0 )) && s=0
  local d=$(( s / 86400 )) h=$(( s % 86400 / 3600 )) m=$(( s % 3600 / 60 ))
  if (( d > 0 )); then printf '%dd%dh' "$d" "$h"
  elif (( h > 0 )); then printf '%dh%02dm' "$h" "$m"
  else printf '%dm' "$m"; fi
}

# ── Parse input (one jq call; "-" marks missing so tab-splitting stays aligned) ─
IFS=$'\t' read -r model dir proj ctx h5_pct h5_reset w1_pct w1_reset cost dur_ms added removed < <(
  jq -r '
    def epoch: if . == null then "" elif type == "number" then floor
               else (sub("\\.[0-9]+"; "") | sub("\\+00:00$"; "Z") | fromdateiso8601) end;
    [ (.model.display_name // ""),
      (.workspace.current_dir // .cwd // ""),
      (.workspace.project_dir // ""),
      (.context_window.used_percentage // ""),
      (.rate_limits.five_hour.used_percentage // ""),
      (.rate_limits.five_hour.resets_at | epoch),
      (.rate_limits.seven_day.used_percentage // ""),
      (.rate_limits.seven_day.resets_at | epoch),
      (.cost.total_cost_usd // ""),
      (.cost.total_duration_ms // ""),
      (.cost.total_lines_added // ""),
      (.cost.total_lines_removed // "") ]
    | map(if . == "" then "-" else tostring end) | join("\t")' <<<"$input")

# ── Model ─────────────────────────────────────────────────────────────────────
[ "$model" != "-" ] && seg "$MAUVE" "$CRUST" "$BOLD$I_MODEL $model$NOBOLD"

# ── Directory: ~ for home, …/last/two when deep ───────────────────────────────
if [ "$dir" != "-" ]; then
  short=${dir/#$HOME/\~}
  IFS=/ read -ra parts <<<"$short"
  (( ${#parts[@]} > 4 )) && short="…/${parts[-2]}/${parts[-1]}"
  icon=$I_DIR; [ "$short" = "~" ] && icon=$I_HOME
  seg "$BLUE" "$CRUST" "$icon $short"
fi

# ── Git: branch, ahead/behind, staged/modified/untracked (one git call) ───────
if [ "$dir" != "-" ] && gs=$(git -C "$dir" --no-optional-locks status --porcelain=v2 --branch 2>/dev/null); then
  branch="" oid="" ahead=0 behind=0 staged=0 modified=0 untracked=0
  while IFS= read -r l; do
    case $l in
      "# branch.head "*) branch=${l#\# branch.head } ;;
      "# branch.oid "*)  oid=${l#\# branch.oid } ;;
      "# branch.ab "*)   read -r _ _ a b <<<"$l"; ahead=${a#+}; behind=${b#-} ;;
      [12]" "*)          xy=${l:2:2}
                         [ "${xy:0:1}" != . ] && ((staged++))
                         [ "${xy:1:1}" != . ] && ((modified++)) ;;
      "? "*)             ((untracked++)) ;;
    esac
  done <<<"$gs"
  [ "$branch" = "(detached)" ] && branch="➦ ${oid:0:7}"
  info="$I_BRANCH $branch"
  (( ahead ))     && info+=" $I_UP$ahead"
  (( behind ))    && info+=" $I_DOWN$behind"
  (( staged ))    && info+=" +$staged"
  (( modified ))  && info+=" ~$modified"
  (( untracked )) && info+=" ?$untracked"
  if (( staged + modified + untracked )); then seg "$PEACH" "$CRUST" "$info"
  else seg "$SURFACE1" "$TEXT" "$info"; fi
fi

end_row

# ── Context window ────────────────────────────────────────────────────────────
if [ "$ctx" != "-" ]; then
  c=$GREEN
  awk -v p="$ctx" 'BEGIN{exit !(p > 20)}' && c=$YELLOW
  awk -v p="$ctx" 'BEGIN{exit !(p > 50)}' && c=$RED
  seg "$c" "$CRUST" "$I_CTX $(bar "$ctx") $(printf '%.0f' "$ctx")%"
fi

# ── Usage windows ─────────────────────────────────────────────────────────────
# usage <icon> <label> <pct> <reset_epoch> <window_s> <pace_step_s>
usage() {
  local icon=$1 label=$2 pct=$3 reset=$4 window=$5 step=$6
  [ "$pct" = "-" ] && return
  local c=$GREEN when=""
  if [ "$reset" != "-" ]; then
    local elapsed=$(( window - (reset - now) ))
    (( elapsed < 0 )) && elapsed=0
    # Round elapsed up to the next step so pace is judged per slot, not live
    # (otherwise any usage minutes into a window reads as over pace)
    elapsed=$(( (elapsed + step - 1) / step * step ))
    (( elapsed < step )) && elapsed=$step
    # How far ahead of pace, in percentage points: ≤0 green, ≤10 yellow, else red
    local over=$(awk -v p="$pct" -v e="$elapsed" -v w="$window" 'BEGIN{printf "%d", p - e/w*100}')
    (( over > 0 )) && c=$YELLOW
    (( over > 10 )) && c=$RED
    when=" $THIN $(dur $(( reset - now )))"
  fi
  seg "$c" "$CRUST" "$icon $label $(bar "$pct") $(printf '%.0f' "$pct")%$when"
}
usage "$I_5H" 5h "$h5_pct" "$h5_reset" 18000  1800
usage "$I_1W" 1w "$w1_pct" "$w1_reset" 604800 10800

# ── Session: duration, lines changed, cost ────────────────────────────────────
sess=""
[ "$dur_ms" != "-" ] && sess+="$I_CLOCK $(dur $(( ${dur_ms%.*} / 1000 )))"
if [ "$added" != "-" ] && (( added + removed )); then
  sess+=" $(fg "$GREEN")+$added $(fg "$RED")-$removed$(fg "$SUBTEXT")"
fi
[ "$cost" != "-" ] && sess+=" $(printf '$%.2f' "$cost")"
[ -n "$sess" ] && seg "$SURFACE0" "$SUBTEXT" "${sess# }"

end_row
printf '%s' "${line%$'\n'}"
