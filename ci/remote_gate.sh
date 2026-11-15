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
# What is shipped: `git archive HEAD` (the head tree), `git archive <base>` (the base tree, for the jobs that compare
# with the PR base) and three files of the checkout running this script (this script's ci/remote_gate_driver.sh,
# tests/gate_shards.py, ci/test_count_gate.py). Nothing else: no .git, no gh token, no ~/.ssh, no ~/.claude, no
# FACTORY_HOME. The remote side runs under `env -i` with an explicit allow-list (ci/remote_gate_driver.sh).
#
# Step -> tests.yml job (all steps run, failures do not stop later steps, like fail-fast: false):
#   toolchain             (setup-python 3.13, setup-go go.mod toolchain, apt zsh: checked, never installed)
#   py-compile            unittest-shard: python -m py_compile bin/factory bin/factory_cli.py
#   factory-help          unittest-shard: python bin/factory --help
#   unittest              unittest-shard (1..10) + unittest fan-in "every module ran exactly once":
#                         $FACTORY_MAIN/tests/gate_shards.py (ci/shard.py run + verify) with GATE_SHARDS <= 8
#   go-modules            go: setup-go module download (go mod download; the only step with network, GOPROXY default)
#   go-gofmt              go: gofmt -l .
#   go-vet                go: go vet ./...
#   go-test               go: go test -race -count=1 -timeout 30m ./...   (-p 8)
#   go-build              go: go build ./...
#   guard-build           guard-equiv / guard-equiv-rest: "Build worker-guard once" (GUARD_GO_BIN); guard steps run with
#                         GOFLAGS=-mod=readonly GOPROXY=off, each in its own copy of the tree, <= 8 at once
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
# Env: GATE_REMOTE_HOST (ssh host, required), GATE_SHARDS (runner; capped at 8), GATE_BASE (base rev; default the
# merge base of HEAD with refs/factory-judge/main, else origin/main; HEAD^ when that is HEAD; none: refused).
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

base=
if [ -n "${GATE_BASE-}" ]; then
  base=$(git rev-parse --verify "${GATE_BASE}^{commit}")
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

ssh_opts=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ServerAliveCountMax=4
          -o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes -o SendEnv=-* -o RequestTTY=no)
rssh() { ssh "${ssh_opts[@]}" "$host" "$@"; }

ts=$(date -u +%Y%m%dT%H%M%SZ)
rdir="factory-gate/${head:0:12}-$ts-$$"   # relative to the remote home
cleaned=0
cleanup() {
  [ "$cleaned" = 1 ] && return 0
  cleaned=1
  if rssh "rm -rf -- \"\$HOME/$rdir\""; then
    echo "remote-gate: removed $host:~/$rdir"
  else
    echo "remote-gate: WARNING: could not remove $host:~/$rdir (the next run's sweep removes dirs older than 12 h)" >&2
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

echo "remote-gate: head $head base $base host $host dir ~/$rdir shards $shards"
t0=$(date +%s)
rssh "mkdir -p \"\$HOME/$rdir/head\" \"\$HOME/$rdir/base\" \"\$HOME/$rdir/trusted\""
git archive --format=tar HEAD | gzip -1 | rssh "tar -xzf - -C \"\$HOME/$rdir/head\""
git archive --format=tar "$base" | gzip -1 | rssh "tar -xzf - -C \"\$HOME/$rdir/base\""
tar -C "$main_root" -czf - ci/remote_gate_driver.sh tests/gate_shards.py ci/test_count_gate.py \
  | rssh "tar -xzf - -C \"\$HOME/$rdir/trusted\""
echo "remote-gate: shipped in $(( $(date +%s) - t0 )) s"

# Remote PATH: the host's own login PATH is not used; only system dirs plus the go toolchains the driver looks for.
rc=0
rssh "env -i HOME=\"\$HOME\" LANG=C.UTF-8 PATH=/usr/local/bin:/usr/bin:/bin bash \"\$HOME/$rdir/trusted/ci/remote_gate_driver.sh\" \"\$HOME/$rdir\" $head $base $shards" || rc=$?
echo "remote-gate: remote exit $rc, total wall $(( $(date +%s) - t0 )) s"
exit "$rc"
