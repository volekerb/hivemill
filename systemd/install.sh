#!/usr/bin/env bash
# Install the hivemill systemd user units (idempotent). Usage: install.sh [--dry-run] [--disable-herdr-agent-resume]
# Links hivemill.service and the digest and health services and timers from this checkout into
# $XDG_CONFIG_HOME/systemd/user and enables hivemill.service, hivemill-digest.timer and
# hivemill-health.timer for the next boot. It never starts or restarts a unit, and never uses --now.
set -euo pipefail

dry=0
disable_resume=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry=1 ;;
    --disable-herdr-agent-resume) disable_resume=1 ;;
    *) echo "usage: $0 [--dry-run] [--disable-herdr-agent-resume]" >&2; exit 2 ;;
  esac
done

src=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
dest="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
units=(hivemill.service hivemill-digest.service hivemill-digest.timer hivemill-health.service hivemill-health.timer)
enable=(hivemill.service hivemill-digest.timer hivemill-health.timer)

run() {
  if ((dry)); then echo "[dry-run] $*"; else "$@"; fi
}

# Opt-in (owner decision): herdr's resume is global, so it also stops resuming the owner's own claude
# panes. With it off, herdr restores plain shells. It only edits herdr's config; no unit is touched.
disable_herdr_resume() {
  local cfg="${XDG_CONFIG_HOME:-$HOME/.config}/herdr/config.toml" key=resume_agents_on_restore
  if [[ -f "$cfg" ]] && awk -v k="$key" '/^[[:space:]]*\[/ {s=($0 ~ /^[[:space:]]*\[session\][[:space:]]*(#.*)?$/)} s && $0 ~ "^[[:space:]]*" k "[[:space:]]*=[[:space:]]*false[[:space:]]*(#.*)?$" {f=1} END {exit !f}' "$cfg"; then
    echo "ok: $cfg already has [session] $key = false"
    return
  fi
  local bak="$cfg.bak-$(date +%Y%m%d%H%M%S)"
  if ((dry)); then
    echo "[dry-run] set [session] $key = false in $cfg (backup $bak)"
    return
  fi
  mkdir -p "$(dirname "$cfg")"
  if [[ ! -f "$cfg" ]]; then
    printf '[session]\n%s = false\n' "$key" > "$cfg"
    echo "created $cfg with [session] $key = false"
    return
  fi
  cp -p "$cfg" "$bak"
  awk -v k="$key" '
    function flush() { if (insess && !done) { print k " = false"; done = 1 } }
    /^[[:space:]]*\[/ { flush(); insess = ($0 ~ /^[[:space:]]*\[session\][[:space:]]*(#.*)?$/); if (insess) seen = 1; print; next }
    insess && $0 ~ "^[[:space:]]*" k "[[:space:]]*=" { if (!done) { print k " = false"; done = 1 } next }
    { print }
    END { flush(); if (!seen) { print ""; print "[session]"; print k " = false" } }
  ' "$bak" > "$cfg.tmp" && mv "$cfg.tmp" "$cfg"
  echo "set [session] $key = false in $cfg (backup $bak)"
}

if ((disable_resume)); then
  disable_herdr_resume
fi

if [[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || true)" != yes ]]; then
  echo "WARNING: linger is not enabled for $USER; user units will not start at boot." >&2
  echo "         enable it with: loginctl enable-linger $USER" >&2
fi

case "$src" in
  */worktrees/*) echo "WARNING: installing from a worktree ($src); the links dangle once it is cleaned. Run it from the main checkout." >&2 ;;
esac

run mkdir -p "$dest"
for u in "${units[@]}"; do
  if [[ -L "$dest/$u" && "$(readlink "$dest/$u")" == "$src/$u" ]]; then
    echo "ok: $dest/$u already linked"
    continue
  fi
  if [[ -e "$dest/$u" && ! -L "$dest/$u" ]] && ! cmp -s "$src/$u" "$dest/$u"; then
    bak="$dest/$u.bak"
    [[ -e "$bak" ]] && bak="$bak.$(date +%Y%m%d%H%M%S)"  # never overwrite an earlier backup
    run mv "$dest/$u" "$bak"  # keep a differing hand-edited unit
  fi
  run ln -sfn "$src/$u" "$dest/$u"
done

run systemctl --user daemon-reload
for u in "${enable[@]}"; do
  run systemctl --user enable "$u"
done
echo "done. Units are enabled for the next boot; nothing was started or restarted."
