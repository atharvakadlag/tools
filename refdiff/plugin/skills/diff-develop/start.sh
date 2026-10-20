#!/usr/bin/env bash
# Start (or restart) a background refdiff server: <base> (default origin/develop) → working tree, live-updating.
#
#   start.sh [base-ref]   start/restart and open the browser
#   start.sh --stop       stop the server for this repo
set -euo pipefail

version=0.1.0
repo=atharvakadlag/tools

# Prefer refdiff on PATH; otherwise fetch the pinned release binary into a cache dir.
find_refdiff() {
  command -v refdiff && return
  local dir="${XDG_CACHE_HOME:-$HOME/.cache}/refdiff/$version" bin
  bin="$dir/refdiff"
  [[ -x $bin ]] && { echo "$bin"; return; }
  local target
  target="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  if [[ $target != darwin-arm64 ]]; then
    echo "no prebuilt refdiff for $target; build it from https://github.com/$repo/tree/main/refdiff and put it on PATH" >&2
    return 1
  fi
  echo "downloading refdiff ${version}..." >&2
  mkdir -p "$dir"
  curl -fsSL "https://github.com/$repo/releases/download/refdiff-v$version/refdiff-$version-$target.tar.gz" | tar -xzf - -C "$dir" refdiff
  chmod +x "$bin"
  echo "$bin"
}

root=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not inside a git repository" >&2; exit 1; }
# Stable port per repo so re-runs reuse the same URL.
port=$(( 20000 + $(printf '%s' "$root" | cksum | cut -d' ' -f1) % 10000 ))
log="${TMPDIR:-/tmp}/refdiff-$port.log"

pids=$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true)
[[ -n $pids ]] && kill $pids 2>/dev/null || true
if [[ ${1:-} == --stop ]]; then
  if [[ -n $pids ]]; then echo "stopped refdiff on port $port"; else echo "no refdiff running for $root"; fi
  exit 0
fi

base=${1:-origin/develop}
git -C "$root" rev-parse --verify -q "$base" >/dev/null || { echo "$base not found (try: git fetch origin ${base#origin/})" >&2; exit 1; }

refdiff=$(find_refdiff)

nohup "$refdiff" --port "$port" -C "$root" "$base" >"$log" 2>&1 &
disown

for _ in $(seq 1 50); do
  url=$(grep -m1 -o 'http://[^ ]*' "$log" 2>/dev/null || true)
  [[ -n $url ]] && break
  sleep 0.1
done
[[ -n ${url:-} ]] || { echo "refdiff failed to start:" >&2; cat "$log" >&2; exit 1; }

echo "compare: $base → $(git -C "$root" rev-parse --abbrev-ref HEAD) + uncommitted changes"
echo "url:     $url"
echo "log:     $log"
