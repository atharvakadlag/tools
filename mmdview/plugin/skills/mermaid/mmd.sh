#!/usr/bin/env bash
# Drive an mmdview server: live-reloading Mermaid viewer in the browser.
#
#   mmd.sh dir                 print the default diagrams directory
#   mmd.sh show <file>         start a server if needed, focus <file> in the browser, print its render status
#   mmd.sh check <file>        print the render status of <file>'s current contents
#   mmd.sh start [dir]         start (if not running) a server for dir and open the browser
#   mmd.sh stop [dir]          stop the server for dir
#
# Files under the default dir use the default server; anything else is served from its git root (or its directory).
set -euo pipefail

version=0.1.0
repo=atharvakadlag/tools
default_dir="${MMDVIEW_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/mmdview/diagrams}"

# find_mmdview: mmdview on PATH, else the pinned release binary (downloaded once into the cache dir).
find_mmdview() {
  command -v mmdview && return
  local dir="${XDG_CACHE_HOME:-$HOME/.cache}/mmdview/$version" bin target
  bin="$dir/mmdview"
  [[ -x $bin ]] && { echo "$bin"; return; }
  target="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  case $target in darwin-arm64 | darwin-amd64 | linux-amd64 | linux-arm64) ;;
  *) echo "no prebuilt mmdview for $target; build it from https://github.com/$repo/tree/main/mmdview and put it on PATH" >&2; return 1 ;;
  esac
  echo "downloading mmdview ${version}..." >&2
  mkdir -p "$dir"
  curl -fsSL "https://github.com/$repo/releases/download/mmdview-v$version/mmdview-$version-$target.tar.gz" | tar -xzf - -C "$dir" mmdview
  chmod +x "$bin"
  echo "$bin"
}

abspath() { (cd "$(dirname "$1")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$1")"); }

# root_for <file>: the directory a server for this file should serve.
root_for() {
  local f d
  f=$(abspath "$1")
  d=$(mkdir -p "$default_dir" && cd "$default_dir" && pwd -P)
  [[ $f == "$d"/* ]] && { echo "$d"; return; }
  git -C "$(dirname "$f")" rev-parse --show-toplevel 2>/dev/null || dirname "$f"
}

port_for() { echo $((40000 + $(printf '%s' "$1" | cksum | cut -d' ' -f1) % 10000)); }

running() { curl -fs -m 1 "http://127.0.0.1:$1/api/files" 2>/dev/null | grep -qF "\"root\":\"$2\""; }

open_url() {
  case "$(uname -s)" in
  Darwin) open "$1" ;;
  *) xdg-open "$1" >/dev/null 2>&1 || echo "open $1 in a browser" >&2 ;;
  esac
}

# ensure <root>: start a server for root unless one is already up; prints the port.
ensure() {
  local root=$1 port log bin pids sum
  port=$(port_for "$root")
  bin=$(find_mmdview)
  log="${TMPDIR:-/tmp}/mmdview-$port.log"
  sum=$(cksum <"$bin")
  # reuse a running server unless the binary was rebuilt since it started (open pages reload on restart)
  running "$port" "$root" && [[ $(cat "$log.bin" 2>/dev/null) == "$sum" ]] && { echo "$port"; return; }
  pids=$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true)
  [[ -n $pids ]] && { kill $pids 2>/dev/null || true; sleep 0.2; }
  echo "$sum" >"$log.bin"
  nohup "$bin" --no-open --port "$port" "$root" >"$log" 2>&1 &
  disown
  for _ in $(seq 1 50); do
    running "$port" "$root" && { echo "$port"; return; }
    sleep 0.1
  done
  echo "mmdview failed to start:" >&2
  cat "$log" >&2
  return 1
}

viewers() { curl -fs -m 2 "http://127.0.0.1:$1/api/status" | grep -o '"viewers":[0-9]*' | cut -d: -f2; }

rel() { local f; f=$(abspath "$2"); echo "${f#"$1"/}"; }

urlencode() { python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$1"; }

status() {
  local port=$1 path=$2 wait=${3:-8}
  curl -fs -m $((wait + 5)) "http://127.0.0.1:$port/api/status?wait=$wait&path=$(urlencode "$path")"
  echo
}

cmd=${1:-}
shift || true
case $cmd in
dir)
  mkdir -p "$default_dir"
  (cd "$default_dir" && pwd -P)
  ;;
show | check)
  [[ $# -ge 1 ]] || { echo "usage: mmd.sh $cmd <file>" >&2; exit 1; }
  [[ -f $1 ]] || { echo "no such file: $1" >&2; exit 1; }
  root=$(root_for "$1")
  port=$(ensure "$root")
  path=$(rel "$root" "$1")
  url="http://127.0.0.1:$port/#$(urlencode "$path")"
  if [[ $cmd == show ]]; then
    if [[ $(viewers "$port") == 0 ]]; then
      open_url "$url"
    else
      curl -fs -X POST "http://127.0.0.1:$port/api/open?path=$(urlencode "$path")" >/dev/null || true
    fi
    echo "url:    $url"
  fi
  printf 'status: '
  status "$port" "$path"
  ;;
start)
  root=$(cd "${1:-$default_dir}" 2>/dev/null && pwd -P) || { mkdir -p "$default_dir"; root=$(cd "$default_dir" && pwd -P); }
  port=$(ensure "$root")
  url="http://127.0.0.1:$port/"
  [[ $(viewers "$port") == 0 ]] && open_url "$url"
  echo "serving: $root"
  echo "url:     $url"
  echo "log:     ${TMPDIR:-/tmp}/mmdview-$port.log"
  ;;
stop)
  root=$(cd "${1:-$default_dir}" && pwd -P)
  port=$(port_for "$root")
  pids=$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true)
  if [[ -n $pids ]]; then kill $pids; echo "stopped mmdview for $root"; else echo "no mmdview running for $root"; fi
  ;;
*)
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
  ;;
esac
