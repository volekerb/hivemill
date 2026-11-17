#!/usr/bin/env bash
# Remote half of ci/remote_gate.sh (T-1633). Runs ON the remote host under `env -i`, shipped from the main checkout.
#   remote_gate_driver.sh <run dir> <head sha> <base sha> <shards>
#   remote_gate_driver.sh --plan     print the step plan (no work)
# <run dir> holds head/ (the tracked files of HEAD), base/ (git archive of the base) and trusted/ (this file, gate_shards.py,
# test_count_gate.py). Everything the run writes stays under <run dir>, which is removed on every exit path; only the
# shared Go module and build caches live outside it (~/go/pkg/mod, ~/.cache/factory-gate/go-build).
# One run at a time: flock on ~/factory-gate/.lock (a second run waits). At most 8 CPUs: taskset -c 0-7, GOMAXPROCS,
# go test -p, GATE_SHARDS, at most 8 parallel guard jobs.
set -euo pipefail

# Keep in run order and in step with the step map in ci/remote_gate.sh (tests/test_gate_shards.py checks both).
PLAN="toolchain py-compile factory-help unittest go-modules go-gofmt go-vet go-test go-build guard-build
guard-unittest-1 guard-unittest-2 guard-unittest-3 guard-equiv-rest guard-equiv-1 guard-equiv-2 guard-equiv-3
guard-equiv-4 guard-diff guard-self-compare guard-home-loosen docs-check"
if [ "${1-}" = --plan ]; then
  for s in $PLAN; do echo "$s"; done
  exit 0
fi
if [ "$#" -ne 4 ]; then echo "remote-gate-driver: usage: remote_gate_driver.sh <run dir> <head> <base> <shards>" >&2; exit 2; fi
run=$1 head_sha=$2 base_sha=$3 shards=$4
case "$head_sha$base_sha" in *[!0-9a-f]*) echo "remote-gate-driver: head/base must be hex shas" >&2; exit 2 ;; esac
case "$shards" in ''|*[!0-9]*) echo "remote-gate-driver: shards must be an integer" >&2; exit 2 ;; esac
[ -d "$run/head" ] && [ -d "$run/base" ] || { echo "remote-gate-driver: $run/head or $run/base missing" >&2; exit 2; }

ncpu=$(nproc)
cores=8; [ "$ncpu" -lt "$cores" ] && cores=$ncpu
[ "$shards" -gt "$cores" ] && shards=$cores
# Re-exec once in a new session (so cleanup can kill the whole process group), pinned to $cores CPUs and niced.
if [ "${REMOTE_GATE_LEADER-}" != 1 ]; then
  # The watch target is our sshd ancestor: it exits when the connection drops (a waiting shell in between would not).
  w=$PPID
  while [ "$w" -gt 1 ] && [ -r "/proc/$w/comm" ]; do
    case "$(cat "/proc/$w/comm")" in sshd*) break ;; esac
    w=$(awk '{print $4}' "/proc/$w/stat")
  done
  if [ "$w" -le 1 ] || [ ! -r "/proc/$w/comm" ]; then w=$PPID; fi
  pin=(); command -v taskset >/dev/null && pin=(taskset -c "0-$((cores - 1))")
  export REMOTE_GATE_LEADER=1 REMOTE_GATE_WATCH_PID=$w
  exec setsid -w "${pin[@]}" nice -n 5 bash "$0" "$@"
fi

real_home=$HOME
cleanup() {
  rc=$?
  trap - EXIT
  trap '' INT TERM HUP PIPE
  kill -TERM -- "-$$" 2>/dev/null || :   # every process of this run (watchdog, monitor, jobs, their children)
  sleep 1
  rm -rf -- "$run" || rm -rf -- "$run" || :
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP PIPE

# Lost ssh connection: the sshd ancestor goes away; end the run (and so clean up) instead of running on unseen.
( while kill -0 "$REMOTE_GATE_WATCH_PID" 2>/dev/null; do sleep 5; done; kill -TERM $$ 2>/dev/null ) &

mkdir -p "$real_home/factory-gate"
exec 9>"$real_home/factory-gate/.lock"
if ! flock -n 9; then
  echo "remote-gate-driver: another gate run holds ~/factory-gate/.lock; waiting"
  flock 9
fi
# Sweep dirs of runs that died without cleanup (only under the lock, so never a live run's dir; > 12 h old).
find "$real_home/factory-gate" -mindepth 1 -maxdepth 1 -type d -mmin +720 ! -path "$run" -exec rm -rf -- {} + || :

# Sampled machine load for the report, every 5 s: CPU busy % (all CPUs), used RAM MiB (MemTotal-MemAvailable), load1.
mon="$run/monitor.txt"
( read -r _ a b c d e f g _ < /proc/stat; pt=$((a+b+c+d+e+f+g)); pi=$((d+e))
  while sleep 5; do
    read -r _ a b c d e f g _ < /proc/stat; t=$((a+b+c+d+e+f+g)); i=$((d+e))
    mem=$(awk '/^MemTotal/{t=$2} /^MemAvailable/{a=$2} END{print int((t-a)/1024)}' /proc/meminfo)
    dt=$((t - pt)); busy=0; [ "$dt" -gt 0 ] && busy=$(( 100 * (dt - (i - pi)) / dt ))
    echo "$busy $mem $(cut -d' ' -f1 /proc/loadavg)"
    pt=$t; pi=$i
  done ) > "$mon" &
mon_pid=$!

# Allow-listed env for every step (the caller already used env -i).
go_root=
for g in "$real_home/.local/go" /usr/local/go /usr/lib/go; do
  if [ -x "$g/bin/go" ]; then go_root=$(cd "$g" && pwd -P); break; fi
done
export HOME="$run/home" TMPDIR="$run/tmp" FACTORY_HOME="$run/factory-home" GATE_REPORT_DIR="$run/reports"
export FACTORY_TEST_SLOT_HELD=1 GATE_SHARDS="$shards" GOMAXPROCS="$cores" GOTOOLCHAIN=local LANG=C.UTF-8
export GOPATH="$real_home/go" GOMODCACHE="$real_home/go/pkg/mod" GOCACHE="$real_home/.cache/factory-gate/go-build"
export PATH="${go_root:+$go_root/bin:}/usr/local/bin:/usr/bin:/bin"
mkdir -p "$HOME/.local" "$TMPDIR" "$FACTORY_HOME" "$GATE_REPORT_DIR" "$GOCACHE" "$run/logs"
# tests/test_guard_build.py trusts only ~/.local/go/bin/go (CI symlinks the setup-go root the same way).
[ -n "$go_root" ] && ln -sfn "$go_root" "$HOME/.local/go"
git_id=(-c user.name=remote-gate -c user.email=gate@example.invalid -c commit.gpgsign=false)

# The shipped trees have no history: rebuild a two-commit repo (base, then head) so `git diff <base> HEAD` and
# HEAD-relative tools work as in CI's fetch-depth 0 checkout. Commit shas differ from the real ones; the real ones
# are in the log header and in the env as GATE_HEAD_SHA / GATE_BASE_SHA.
export GATE_HEAD_SHA="$head_sha" GATE_BASE_SHA="$base_sha"
( cd "$run/head"
  git init -q -b main .
  git "${git_id[@]}" --work-tree="$run/base" add -A -f
  git "${git_id[@]}" commit -q --no-verify -m "base $base_sha"
  git tag gate-base
  git "${git_id[@]}" add -A -f
  git "${git_id[@]}" commit -q --no-verify --allow-empty -m "head $head_sha"
  git update-ref refs/remotes/origin/main gate-base
  [ -z "$(git status --porcelain --ignored)" ] || { echo "remote-gate-driver: rebuilt head repo is not clean" >&2; exit 1; } )
B=gate-base
T="$run/trusted"
cd "$run/head"

declare -A RC SECS PID
order=()
show() {  # show NAME: the result line, the full log on failure, else its last 3 lines
  echo "===== $1: exit ${RC[$1]}, ${SECS[$1]} s ====="
  if [ "${RC[$1]}" -ne 0 ]; then cat "$run/logs/$1.log"; else tail -n 3 "$run/logs/$1.log"; fi
}
# step NAME CMD...: run in the head tree, output to logs/NAME.log, record rc and seconds, never abort the run.
step() {
  local name=$1; shift
  local s=$SECONDS r=0
  # in the background + wait: a trapped TERM interrupts `wait`, but would wait for a foreground command to end
  "$@" >"$run/logs/$name.log" 2>&1 &
  wait "$!" || r=$?
  RC[$name]=$r; SECS[$name]=$((SECONDS - s)); order+=("$name")
  show "$name"
}
# bg NAME DIR CMD...: start a job in DIR (its own tree copy), at most $cores jobs at once. rc and seconds go to a
# file: a job reaped by `wait -n` cannot be waited for again.
bg() {
  local name=$1 dir=$2; shift 2
  while [ "$(find "$run/logs" -name '*.running' | wc -l)" -ge "$cores" ]; do wait -n || :; done
  : > "$run/logs/$name.running"
  ( s=$SECONDS; r=0; cd "$dir" && "$@" || r=$?
    echo "$r $((SECONDS - s))" > "$run/logs/$name.done"; rm -f "$run/logs/$name.running"; exit "$r"
  ) >"$run/logs/$name.log" 2>&1 &
  PID[$name]=$!
  order+=("$name")
}
collect() {
  local name r s
  for name in "$@"; do
    wait "${PID[$name]}" || :
    if read -r r s < "$run/logs/$name.done"; then RC[$name]=$r; SECS[$name]=$s; else RC[$name]=99; SECS[$name]=0; fi
    show "$name"
  done
}

echo "remote-gate-driver: $(hostname) cpus $ncpu, using $cores ($(taskset -cp $$ 2>&1 || :)), head $head_sha base $base_sha"

toolchain() {
  local want_go py
  want_go=$(awk '/^toolchain /{print $2}' go/go.mod)
  py=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
  echo "python3 $py (CI: 3.13)"; echo "go: $(go version 2>&1) (CI: $want_go from go/go.mod)"; echo "zsh: $(zsh --version 2>&1)"
  echo "gcc: $(gcc --version 2>&1 | head -1)"; echo "git: $(git --version)"
  [ "$py" = 3.13 ] || { echo "python3 is $py, CI uses 3.13"; return 1; }
  [ "$(go env GOVERSION)" = "$want_go" ] || { echo "go is $(go env GOVERSION), go.mod pins $want_go"; return 1; }
  command -v zsh >/dev/null || { echo "zsh missing (CI installs it)"; return 1; }
  command -v gcc >/dev/null || { echo "gcc missing (go test -race needs cgo)"; return 1; }
}
gofmt_check() {
  local u; u=$(cd go && gofmt -l .)
  if [ -n "$u" ]; then echo "gofmt: unformatted files (run gofmt -w):"; echo "$u"; return 1; fi
  echo "gofmt: clean"
}
corpus_real() {
  local lines; lines=$(wc -l < ci/guard_corpus.json); echo "corpus: ci/guard_corpus.json has $lines lines"
  [ "$lines" -gt 100 ] || { echo "corpus has $lines lines (<= 100): not the real corpus"; return 1; }
}
guard_diff_job() {  # tests.yml guard-diff, tool and base corpus from the base tree
  set -o pipefail
  corpus_real || return 1
  local gd=ci/guard_diff.py corpus=(--corpus ci/guard_corpus.json) allow=() rc=0 files
  if [ -f "$run/base/ci/guard_diff.py" ] && [ -f "$run/base/tests/_slot.py" ] && [ -f "$run/base/systemd/install.sh" ]; then
    mkdir -p "$TMPDIR/base-tool/ci" "$TMPDIR/base-tool/tests" "$TMPDIR/base-tool/systemd"
    cp "$run/base/ci/guard_diff.py" "$TMPDIR/base-tool/ci/"; cp "$run/base/tests/_slot.py" "$TMPDIR/base-tool/tests/"
    cp "$run/base/systemd/install.sh" "$TMPDIR/base-tool/systemd/"
    gd="$TMPDIR/base-tool/ci/guard_diff.py"; echo "tool: ci/guard_diff.py from base $GATE_BASE_SHA"
  else
    echo "tool: base lacks ci/guard_diff.py, tests/_slot.py or systemd/install.sh, using the PR copy"
  fi
  if [ -f "$run/base/ci/guard_corpus.json" ]; then
    corpus=(--corpus "$run/base/ci/guard_corpus.json" --corpus ci/guard_corpus.json)
  fi
  files=$(git diff --name-only --diff-filter=AM "$B" HEAD -- 'ci/allow_loosen_*.json')
  if [ -n "$files" ]; then
    echo "PLANNED LOOSENING listed by this PR:"; for f in $files; do echo "--- $f"; cat "$f"; done
    python3 -I -c 'import json,sys; print(json.dumps([e for p in sys.argv[1:] for e in json.load(open(p))]))' $files \
      > "$TMPDIR/allow_loosen.json"
    allow=(--allow-loosen "$TMPDIR/allow_loosen.json")
  fi
  python3 "$gd" --base "$B" --repo . "${corpus[@]}" --mode both "${allow[@]}" | tee "$TMPDIR/gd-both.txt" || rc=1
  python3 "$gd" --base "$B" --repo . "${corpus[@]}" --mode worker --role evaluator "${allow[@]}" | tee "$TMPDIR/gd-eval.txt" || rc=1
  grep -E '^(LOOSENED|SETTINGS-LOOSENED|TIMEOUT)' "$TMPDIR/gd-both.txt" "$TMPDIR/gd-eval.txt" || echo "no LOOSENED/TIMEOUT lines"
  echo "guard_diff failing runs: $rc"; return "$rc"
}
self_compare_job() {  # tests.yml guard-self-compare
  set -o pipefail
  corpus_real || return 1
  git diff --exit-code -- roles/hooks/worker-guard.py || return 1
  local rc=0 out="$TMPDIR/gd-self-$$.txt"
  python3 ci/guard_diff.py --base HEAD --repo . --corpus ci/guard_corpus.json --mode worker --role evaluator | tee "$out" || rc=1
  grep -F ' 0 loosened, 0 tightened' "$out" || { echo "expected ' 0 loosened, 0 tightened'"; rc=1; }
  echo "guard_diff evaluator self-compare: rc=$rc"; return "$rc"
}
home_loosen_job() {  # tests.yml guard-evaluator-home-loosen (edits worker-guard.py: runs on its own tree copy)
  corpus_real || return 1
  python3 - <<'PY' || return 1
from pathlib import Path
p = Path("roles/hooks/worker-guard.py")
src = p.read_text()
old = '        if not ok:\n            raise Block("the evaluator may run factory'
new = '        if False:\n            raise Block("the evaluator may run factory'
n = src.count(old)
if n != 1:
    raise SystemExit(f"expected one evaluator home-check if-not-ok block, got {n}")
p.write_text(src.replace(old, new, 1))
PY
  local rc=0 out="$TMPDIR/gd-home-loosen-$$.txt"
  python3 ci/guard_diff.py --base HEAD --repo . --corpus ci/guard_corpus.json --mode worker --role evaluator >"$out" 2>&1 || rc=$?
  cat "$out"
  [ "$rc" -ne 0 ] || { echo "expected non-zero exit from guard_diff"; return 1; }
  grep -F 'LOOSENED base=2 pr=0 worker' "$out" || { echo "expected LOOSENED base=2 pr=0 worker"; return 1; }
  echo "home-check loosen probe: rc=$rc (expected non-zero)"
}
equiv_rest_job() {  # tests.yml guard-equiv-rest
  set -o pipefail
  local rc=0
  python3 ci/guard_impl_unittest.py list --routed-rest
  GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run --routed-rest || rc=1
  python3 -m unittest discover -s tests -p test_guard_build.py -v 2>&1 | tee "$TMPDIR/real-go-$$.txt" || rc=1
  python3 -m unittest tests.test_guard_impl.SeedGuardExportTests.test_go_binary_accepts_the_seeded_export -v 2>&1 \
    | tee -a "$TMPDIR/real-go-$$.txt" || rc=1
  echo "REAL_GO skipped: $(grep -c 'skipped' "$TMPDIR/real-go-$$.txt" || :)"
  return "$rc"
}
docs_job() {  # tests.yml docs-check, tool from the base tree
  if [ -f "$run/base/ci/ci_tier.py" ]; then
    mkdir -p "$TMPDIR/docs-tool/ci"; cp "$run/base/ci/ci_tier.py" "$run/base/ci/affected.py" "$TMPDIR/docs-tool/ci/"
    python3 "$TMPDIR/docs-tool/ci/ci_tier.py" docs-check --root . --base "$B" --head HEAD || return 1
  else
    echo "base has no ci/ci_tier.py: using the PR copy"; python3 ci/ci_tier.py docs-check --root . --base "$B" --head HEAD || return 1
  fi
  python3 -m unittest discover -s tests -p test_docs_index.py
}

# Phase 1 (serial, each step uses up to $cores): CI's unittest-shard and go jobs.
step toolchain toolchain
step py-compile python3 -m py_compile bin/factory bin/factory_cli.py
step factory-help python3 bin/factory --help
step unittest python3 "$T/tests/gate_shards.py"
step go-modules bash -c 'cd go && go mod download'
step go-gofmt gofmt_check
step go-vet bash -c 'cd go && go vet ./...'
step go-test bash -c "cd go && go test -race -count=1 -p $cores -timeout 30m ./..."
step go-build bash -c 'cd go && go build ./...'

# Phase 2 (parallel, <= $cores jobs, each in its own copy of the head repo): the guard jobs, GOPROXY off as in CI.
export GOFLAGS=-mod=readonly GOPROXY=off
mkdir -p "$TMPDIR/bin"
step guard-build bash -c "cd go && go build -o '$TMPDIR/bin/worker-guard' ./cmd/worker-guard"
export GUARD_GO_BIN="$TMPDIR/bin/worker-guard"
export -f corpus_real guard_diff_job self_compare_job home_loosen_job equiv_rest_job
export run B
tree() { cp -a "$run/head" "$run/trees/$1"; echo "$run/trees/$1"; }
mkdir -p "$run/trees"
# Longest first (CI: a guard-unittest shard ~20 min).
for i in 1 2 3; do
  bg "guard-unittest-$i" "$(tree gu$i)" env GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run --shard "$i" --shards 3
done
bg guard-equiv-rest "$(tree ger)" bash -c equiv_rest_job
for i in 1 2 3 4; do
  m=worker; [ "$i" = 1 ] && m=both
  bg "guard-equiv-$i" "$(tree ge$i)" python3 ci/guard_diff.py --impl go --go-bin "$GUARD_GO_BIN" --roles all \
    --role-shard "$i" --role-shards 4 --mode "$m" --strict
done
bg guard-diff "$(tree gd)" bash -c guard_diff_job
bg guard-self-compare "$(tree gsc)" bash -c self_compare_job
bg guard-home-loosen "$(tree ghl)" bash -c home_loosen_job
collect guard-unittest-1 guard-unittest-2 guard-unittest-3 guard-equiv-rest guard-equiv-1 guard-equiv-2 guard-equiv-3 \
  guard-equiv-4 guard-diff guard-self-compare guard-home-loosen
unset GOFLAGS GOPROXY
step docs-check docs_job

kill "$mon_pid" 2>/dev/null || :
wait "$mon_pid" 2>/dev/null || :
echo "===== summary (head $head_sha) ====="
fail=0
for n in "${order[@]}"; do
  printf '%-20s %-5s %6s s\n' "$n" "$([ "${RC[$n]}" = 0 ] && echo ok || echo FAIL)" "${SECS[$n]}"
  [ "${RC[$n]}" = 0 ] || fail=1
done
if [ -s "$mon" ]; then
  awk 'BEGIN{c=0;m=0;l=0} {if($1>c)c=$1; if($2>m)m=$2; if($3>l)l=$3} END{printf "peak cpu busy %d%% of %d cpus, peak used ram %d MiB, peak load1 %s\n", c, '"$ncpu"', m, l}' "$mon"
fi
echo "unittest summary: $(cat "$GATE_REPORT_DIR/summary.json" 2>/dev/null || echo missing)"
echo "remote-gate-driver: $([ "$fail" = 0 ] && echo PASS || echo FAIL), wall ${SECONDS} s"
exit "$fail"
