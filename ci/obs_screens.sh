#!/usr/bin/env bash
# Light and dark screenshots of the `hivemill obs` Spend and Flow pages, from the synthetic fixture in
# go/internal/hm/testdata/obs_fixture/. Nothing of a real factory is read: the server gets a throwaway home and a
# clean environment.
#
#   ci/obs_screens.sh --out DIR      writes spend-light.png spend-dark.png flow-light.png flow-dark.png
#
# CHROME (default ~/.local/bin/chrome-headless-shell) is the headless browser, GO the go binary.
set -euo pipefail

die() { echo "obs_screens: $*" >&2; exit 1; }

[ $# -eq 2 ] && [ "$1" = --out ] && [ -n "$2" ] || die "usage: ci/obs_screens.sh --out DIR"
mkdir -p "$2"
out=$(cd "$2" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$root/go/internal/hm/testdata/obs_fixture
chrome=${CHROME:-$HOME/.local/bin/chrome-headless-shell}
[ -x "$chrome" ] || die "no headless browser at $chrome (set CHROME)"
go=${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}

tmp=$(mktemp -d)
server=
cleanup() {
  if [ -n "$server" ]; then
    kill "$server" 2>/dev/null || true
    wait "$server" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

(cd "$root/go" && GOTOOLCHAIN=local "$go" build -o "$tmp/hivemill" ./cmd/hivemill)

# The throwaway factory home: an empty DB, box-a's log and the log "pulled" from box-b.
home=$tmp/factory
mkdir -p "$tmp/user" "$home/db" "$home/state/hivemill/obs/box-b"
: >"$home/db/factory.db"
# The page shows the last 30 days: move the fixture's dates so its newest day is yesterday (UTC).
python3 -I - "$fixture" "$home/state/hivemill" <<'EOF'
import datetime, pathlib, re, sys

src, dst = map(pathlib.Path, sys.argv[1:])
logs = {"events.jsonl": "events.jsonl", "box-b/events.jsonl": "obs/box-b/events.jsonl"}
texts = {name: (src / name).read_text() for name in logs}
day = re.compile(r'("ts":")(\d{4}-\d\d-\d\d)')
newest = max(datetime.date.fromisoformat(m[2]) for t in texts.values() for m in day.finditer(t))
shift = datetime.datetime.now(datetime.timezone.utc).date() - datetime.timedelta(days=1) - newest
for name, text in texts.items():
    (dst / logs[name]).write_text(day.sub(lambda m: m[1] + str(datetime.date.fromisoformat(m[2]) + shift), text))
EOF

# env -i: no FACTORY_*, coordinator or HOME of the caller reaches the server.
hm() { exec env -i PATH="$PATH" HOME="$tmp/user" FACTORY_HOME="$home" "$tmp/hivemill" "$@"; }
# Without machine_name the page would show this machine's hostname.
(hm config machine_name box-a) >"$tmp/config.log" 2>&1 || { cat "$tmp/config.log" >&2; die "config failed"; }

port=$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
url=http://127.0.0.1:$port
hm obs --port "$port" >"$tmp/obs.log" 2>&1 &
server=$!
ready=
for _ in $(seq 100); do
  kill -0 "$server" 2>/dev/null || break
  if curl -fsS -o /dev/null "$url/spend" 2>/dev/null; then ready=1; break; fi
  sleep 0.1
done
[ -n "$ready" ] && kill -0 "$server" 2>/dev/null || { cat "$tmp/obs.log" >&2; die "hivemill obs did not start"; }

# Blink's preferred colour scheme: 0 is dark, 1 is light. The pages follow prefers-color-scheme.
for page in spend flow; do
  curl -fsS -o /dev/null "$url/$page" || die "GET /$page failed"
  for theme in light:1 dark:0; do
    png=$out/$page-${theme%:*}.png
    rm -f "$png"
    "$chrome" --headless --hide-scrollbars --window-size=1280,900 --user-data-dir="$tmp/chrome" \
      --blink-settings=preferredColorScheme="${theme#*:}" --screenshot="$png" "$url/$page" \
      >"$tmp/chrome.log" 2>&1 || { cat "$tmp/chrome.log" >&2; die "screenshot of /$page failed"; }
    [ -s "$png" ] || { cat "$tmp/chrome.log" >&2; die "no screenshot written: $png"; }
  done
  ! cmp -s "$out/$page-light.png" "$out/$page-dark.png" || die "/$page: light and dark screenshots are identical"
done
echo "obs_screens: wrote spend-light.png spend-dark.png flow-light.png flow-dark.png to $out"
