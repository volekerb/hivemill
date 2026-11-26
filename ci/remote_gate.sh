#!/usr/bin/env bash
# Remote merge gate over ssh (T-1633, docs/local-gate.md "Remote gate over ssh").
#
# Runs the coverage of .github/workflows/tests.yml for the tree at HEAD of the current directory on the ssh host
# $GATE_REMOTE_HOST (required; an ssh alias, set in the DB gate_cmd, never in this repo). Meant as a project gate_cmd,
# run from the main checkout so a PR cannot edit its own gate:
#     gate_cmd:       GATE_REMOTE_HOST=<ssh alias> bash $FACTORY_MAIN/ci/remote_gate.sh
#     gate_count_cmd: bash $FACTORY_MAIN/ci/remote_gate.sh --count
#
# Usage: remote_gate.sh            ship HEAD, run every step remotely, exit 0 only if all passed
#        remote_gate.sh --count    print the unittest count of the tree (ci/test_count_gate.py count) as the last line
#        remote_gate.sh --plan     print the step plan and exit (no ssh)
#        remote_gate.sh --help
#
# What is shipped: the tracked files of HEAD (`git ls-files -z | tar --null --no-recursion -T -`, not `git archive`, so the
# PR's own .gitattributes export-ignore / export-subst cannot hide or rewrite files; refused unless HEAD is still the
# recorded head and the index and worktree are clean), `git archive <base>` (the trusted base tree, for the jobs that
# compare with the PR base) and three files of the checkout running this script (this script's ci/remote_gate_driver.sh,
# tests/gate_shards.py, ci/test_count_gate.py). Nothing else: no .git, no gh token, no ~/.ssh, no ~/.claude, no
# FACTORY_HOME. The remote side runs under `env -i` with an explicit allow-list (ci/remote_gate_driver.sh).
#
# Step -> tests.yml job (all steps run, failures do not stop later steps, like fail-fast: false):
#   toolchain             (setup-python 3.13, setup-go go.mod toolchain, apt zsh: checked, never installed)
#   unittest              unittest-shard (1..10) + unittest fan-in "every module ran exactly once":
#                         $FACTORY_MAIN/tests/gate_shards.py (ci/shard.py run + verify) with GATE_SHARDS <= the job cap
#   go-modules            go: setup-go module download (go mod download; the only step with network, GOPROXY default)
#   go-gofmt              go: gofmt -l .
#   go-vet                go: go vet ./...
#   go-test               go: go test -race -count=1 -timeout 30m ./...   (-p = the job cap below)
#   go-build              go: go build ./...
#   guard-build           guard-equiv / guard-equiv-rest: "Build worker-guard once" (GUARD_GO_BIN); guard steps run with
#                         GOFLAGS=-mod=readonly GOPROXY=off, each in its own copy of the tree, <= the job cap at once
#   guard-unittest-1..3   guard-unittest-shard (1..3) + guard-unittest fan-in: GUARD_IMPL=diff ci/guard_impl_unittest.py
#   guard-diff            guard-diff: base-tool guard_diff.py --mode both and --mode worker --role evaluator
#   guard-self-compare    guard-self-compare
#   guard-home-loosen     guard-evaluator-home-loosen (on its own copy of the tree: the probe edits worker-guard.py)
#   guard-equiv-1..4      guard-equiv-shard (1..4) + guard-equiv fan-in: guard_diff.py --impl go --strict
#   guard-equiv-rest      guard-equiv-rest: routed modules under GUARD_IMPL=diff + REAL_GO build tests
#   docs-check            docs-check: base-tool ci_tier.py docs-check + tests/test_docs_index.py
# Deliberately stronger than CI: no tier (CI runs only affected modules / skips go on some PRs; here everything runs
# every time) and the path filters of guard-diff / guard-equiv / guard-equiv-rest are not applied (they always run).
# Not covered, with reason:
#   ci-tier               classification only; the gate always runs the full set, so it has nothing to decide.
#   unittest test-count   the runner does it: gate_count_cmd (--count) on head and on main, tests-removed: bypass.
#   upload/download-artifact, GITHUB_STEP_SUMMARY: GitHub plumbing, no test.
#
# Env: GATE_REMOTE_HOST (ssh host, or `local` to run here without ssh; required), GATE_SHARDS (runner; capped at 8), GATE_BASE (base rev; default the
# merge base of HEAD with refs/factory-judge/main, else origin/main; HEAD^ when that is HEAD; none: refused).
# GATE_RUN_ROOTS: space-separated run root candidates (default "/var/tmp /tmp").
# GATE_TIMEOUT_S: this gate's time limit (the runners set it from projects.gate_timeout_s; default 3600): once this run has
# held the gate lock longer than that, or its driver is dead, the next run stops it and takes the lock.
# Run dir: <root>/fg-XXXXXX on the gate host (mktemp), HOME, TMPDIR, GOTMPDIR and FACTORY_HOME inside it (T-1679);
# <root> is the first of /var/tmp, /tmp that is on disk (not tmpfs) and short enough for unix socket paths.
# Job cap (T-1679): the driver runs min(GATE_SHARDS, 8 CPUs, MemAvailable / 2048 MiB, MemTotal / 2560 MiB) jobs at once
# (unittest shards, go test -p, guard jobs): 4 on an 8-CPU / 12 GB host, so the run never fills swap.
set -euo pipefail

MAX_CORES=8
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
main_root=$(cd "$here/.." && pwd)
host=${GATE_REMOTE_HOST-}

usage() { sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

mode=run
case "${1-}" in
  "") ;;
  --count) mode=count ;;
  --plan) mode=plan ;;
  -h|--help) usage; exit 0 ;;
  *) echo "remote-gate: unknown argument '$1' (use --count, --plan or --help)" >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then echo "remote-gate: one argument at most, got $#" >&2; exit 2; fi

if [ "$mode" = plan ]; then
  exec bash "$here/remote_gate_driver.sh" --plan
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "remote-gate: $(pwd) is not a git work tree; run it from the gate worktree" >&2; exit 2
fi
cd "$(git rev-parse --show-toplevel)"   # ls-files and the count read the whole tree, not a subdir
head=$(git rev-parse --verify HEAD)
dirty=$(git status --porcelain --untracked-files=normal)
if [ -n "$dirty" ]; then
  echo "remote-gate: refusing: the tree at $head has uncommitted or untracked changes (git status --porcelain):" >&2
  printf '%s\n' "$dirty" | head -20 >&2
  exit 2
fi

if [ "$mode" = count ]; then
  # Discovery count of the head tree, last line a bare integer (the runner's gate_count_cmd contract).
  exec python3 "$here/test_count_gate.py" count .
fi

shards=${GATE_SHARDS:-$MAX_CORES}
case "$shards" in ''|*[!0-9]*) echo "remote-gate: GATE_SHARDS='$shards' is not a positive integer" >&2; exit 2 ;; esac
if [ "$shards" -lt 1 ]; then echo "remote-gate: GATE_SHARDS=$shards must be >= 1" >&2; exit 2; fi
if [ "$shards" -gt "$MAX_CORES" ]; then shards=$MAX_CORES; fi
limit=${GATE_TIMEOUT_S:-3600}
case "$limit" in ''|0*|*[!0-9]*) echo "remote-gate: GATE_TIMEOUT_S='$limit' is not a positive number of seconds" >&2; exit 2 ;; esac

base=
if [ -n "${GATE_BASE-}" ]; then
  base=$(git rev-parse --verify --end-of-options "${GATE_BASE}^{commit}")
else
  for ref in refs/factory-judge/main refs/remotes/origin/main; do
    if git rev-parse --verify -q "$ref" >/dev/null; then
      if mb=$(git merge-base HEAD "$ref"); then base=$mb; break; fi
    fi
  done
  if [ -z "$base" ]; then
    # fail closed: a made-up base would let guard-diff / docs-check compare against the wrong tree
    echo "remote-gate: no merge base of HEAD with refs/factory-judge/main or origin/main; set GATE_BASE" >&2; exit 2
  fi
  if [ "$base" = "$head" ]; then  # gating main itself: compare with its parent, as CI's push run does with "before"
    base=$(git rev-parse --verify -q "HEAD^") || { echo "remote-gate: HEAD is main and has no parent; set GATE_BASE" >&2; exit 2; }
  fi
fi
case "$host" in ''|-*|*[!A-Za-z0-9._@-]*) echo "remote-gate: GATE_REMOTE_HOST='$host' is not set or not a plain ssh host name" >&2; exit 2 ;; esac

# No connection sharing: the remote driver watches its sshd, which must end when this client does, not with a shared master.
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ServerAliveCountMax=4
          -o ControlMaster=no -o ControlPath=none -o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes -o SendEnv=-* -o RequestTTY=no)
# GATE_REMOTE_HOST=local: the runner already is on the test host; the same steps run here without ssh (same shipping
# into <run root>/fg-XXXXXX, same env -i, lock, CPU and RAM cap and cleanup).
# GATE_REMOTE_HOST naming the machine this script runs on (its `hostname -s` or `hostname`) means local too: no ssh to
# ourselves, so no ~/.ssh/config alias or host key is needed on the gate host itself (T-1773).
this_short=$(hostname -s 2>/dev/null || :)
this_full=$(hostname 2>/dev/null || :)
if [ "$host" != local ] && { [ "$host" = "$this_short" ] || [ "$host" = "$this_full" ]; }; then
  echo "remote-gate: GATE_REMOTE_HOST=$host is this host: running locally, no ssh"
  host=local
fi
if [ "$host" = local ]; then
  rssh() { bash -c "$*"; }
else
  rssh() { ssh "${ssh_opts[@]}" "$host" "$@"; }
fi

# Short, disk-backed run dir (T-1679): <root>/fg-XXXXXX, <root> picked on the gate host by the driver's --pick-root
# (this checkout's driver, piped into `bash -s`): the first of RUN_ROOTS that is on disk, not tmpfs, so a run (~1 GB with
# go test's build temp) cannot fill a RAM-backed /tmp, and short enough that unix sockets under TMPDIR fit 108 bytes
# (go/internal/sandbox binds <TMPDIR>/<test name>NNN/001/p.sock, up to SOCK_TAIL chars after TMPDIR): TMPDIR is
# /var/tmp/fg-XXXXXX/tmp (22 chars). The old ~/factory-gate/<sha>-<ts>-<pid> was too long for those sockets.
RUN_ROOTS=(/var/tmp /tmp)
# GATE_RUN_ROOTS: space-separated candidate roots instead of the default (a host whose /var/tmp is not writable).
if [ -n "${GATE_RUN_ROOTS-}" ]; then
  case "$GATE_RUN_ROOTS" in *[!A-Za-z0-9._/\ -]*) echo "remote-gate: GATE_RUN_ROOTS has odd characters" >&2; exit 2 ;; esac
  read -r -a RUN_ROOTS <<< "$GATE_RUN_ROOTS"
fi
SOCK_TAIL=82
cleaned=0
rdir=
cleanup() {
  [ "$cleaned" = 1 ] && return 0
  cleaned=1
  [ -n "$rdir" ] || return 0
  if rssh "rm -rf -- '$rdir'"; then
    echo "remote-gate: removed $host:$rdir"
  else
    echo "remote-gate: WARNING: could not remove $host:$rdir (the next run's sweep removes dirs older than 12 h)" >&2
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

t0=$(date +%s)
run_root=$(rssh "bash -s -- --pick-root $SOCK_TAIL ${RUN_ROOTS[*]}" < "$here/remote_gate_driver.sh") || {
  echo "remote-gate: no usable run root on $host among ${RUN_ROOTS[*]}" >&2; exit 2; }
case " ${RUN_ROOTS[*]} " in
  *" $run_root "*) ;;
  *) echo "remote-gate: the run root pick on $host said '$run_root', not one of ${RUN_ROOTS[*]}" >&2; exit 2 ;;
esac
made=$(rssh "mktemp -d $run_root/fg-XXXXXX")
case "$made" in
  "$run_root"/fg-??????) rdir=$made ;;
  *) echo "remote-gate: could not create a run dir under $host:$run_root (mktemp said '$made')" >&2; exit 2 ;;
esac
echo "remote-gate: head $head base $base host $host dir $rdir shards $shards"
rssh "mkdir -p '$rdir/head' '$rdir/base' '$rdir/trusted'"
# Ship the head as its tracked files, read from the worktree. Re-check right before shipping that the worktree still is
# exactly the recorded head: HEAD unmoved, nothing staged, modified, deleted (the files tar reads are HEAD's).
now=$(git rev-parse --verify HEAD)
if [ "$now" != "$head" ]; then
  echo "remote-gate: refusing: HEAD moved from $head to $now before shipping; nothing shipped" >&2; exit 2
fi
dirty=$(git status --porcelain --untracked-files=no)
if [ -n "$dirty" ]; then
  echo "remote-gate: refusing: the index or worktree changed before shipping (git status --porcelain); nothing shipped:" >&2
  printf '%s\n' "$dirty" | head -20 >&2
  exit 2
fi
git ls-files -z | tar --null --no-recursion -T - -cf - | gzip -1 | rssh "tar -xzf - -C '$rdir/head'"
git archive --format=tar --end-of-options "$base" | gzip -1 | rssh "tar -xzf - -C '$rdir/base'"
tar -C "$main_root" -czf - ci/remote_gate_driver.sh tests/gate_shards.py ci/test_count_gate.py \
  | rssh "tar -xzf - -C '$rdir/trusted'"
echo "remote-gate: shipped in $(( $(date +%s) - t0 )) s"

# Remote PATH: the host's own login PATH is not used; only system dirs plus the go toolchains the driver looks for.
rc=0
# Local mode: the driver also watches this script (over ssh the lost connection is what it sees).
script_pid=
if [ "$host" = local ]; then script_pid=$$; fi
rssh "env -i HOME=\"\$HOME\" LANG=C.UTF-8 PATH=/usr/local/bin:/usr/bin:/bin REMOTE_GATE_LIMIT_S=$limit REMOTE_GATE_SCRIPT_PID=$script_pid bash '$rdir/trusted/ci/remote_gate_driver.sh' '$rdir' $head $base $shards" || rc=$?
echo "remote-gate: remote exit $rc, total wall $(( $(date +%s) - t0 )) s"
exit "$rc"
