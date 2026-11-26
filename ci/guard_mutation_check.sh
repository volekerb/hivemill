#!/usr/bin/env bash
# Guard mutation check (T-1963): prove the ported guard tests catch a broken rule.
#   guard_mutation_check.sh PATCH TESTREGEX
#   guard_mutation_check.sh --self-test   (T-1957: runs check on a tiny module with a breaking and a comment-only patch)
# PATCH is a git-style diff (a/ b/ paths, repo-relative) that breaks a guard rule. The tests matching TESTREGEX must
# pass before the patch and fail with it applied. The patch is applied in place and undone on exit; no tree copy.
set -u

PATH=$PATH:${HOME:-}/.local/go/bin # go from PATH when present, else ~/.local/go/bin/go

gotest() { # DIR REGEX
	(cd "$1/go" && GOTOOLCHAIN=local go test -count=1 ./internal/guard -run "$2" 2>&1)
}

undo() { # EXIT trap: reverse a fully applied patch; a part-applied one (the dry run passed) is only reported
	if [ "$_applied" = 1 ] && printf %s "$_patch" | patch -R -p1 -d "$_dir" --forward --batch --fuzz=0 \
		--no-backup-if-mismatch -r - > /dev/null; then return; fi
	echo "undo failed: $_dir may be left patched; restore the files the patch names" >&2; exit 3
}

# check DIR PATCH REGEX: steps 1-3 against the tree in DIR (DIR holds go/). Reused by T-1957.
check() {
	local regex=$3 out
	_dir=$1 _patch=$(cat "$2"; echo x) # read once: PATCH may be a pipe such as /dev/stdin; x keeps trailing newlines
	_patch=${_patch%x}
	if ! out=$(gotest "$_dir" "$regex"); then
		tail -20 <<< "$out"; echo "baseline tests fail"; exit 1
	fi
	if grep -q 'no tests to run' <<< "$out"; then
		echo "baseline tests fail: no test matches $regex"; exit 1
	fi
	# The dry run first, so a patch that fails half way leaves no hunk applied.
	if ! printf %s "$_patch" | patch -p1 -d "$_dir" --forward --batch --fuzz=0 --dry-run; then
		echo "patch does not apply"; exit 1
	fi
	_applied=0; trap undo EXIT # set before the real apply, so a part-applied patch is reported
	if ! printf %s "$_patch" | patch -p1 -d "$_dir" --forward --batch --fuzz=0 --no-backup-if-mismatch -r - > /dev/null; then
		echo "patch does not apply"; exit 1
	fi
	_applied=1
	if out=$(gotest "$_dir" "$regex"); then
		echo "tests still pass with the patch: the ported tests miss this rule"; exit 1
	fi
	tail -20 <<< "$out"
	# Caught only when the test binary ran and failed ("FAIL<tab>pkg<tab>0.01s"), whether by a failing test, a panic,
	# a fatal error or an exit; a build, setup or go-command error has no such line.
	if ! grep -qE $'^FAIL\t[^\t]+\t[0-9.]+s$' <<< "$out"; then
		echo "go test failed without a failing test: the patch breaks the build"; exit 1
	fi
	echo "mutation caught"; exit 0
}

self_test() { # build a tiny module, then check must catch break.patch and must reject comment.patch
	local d
	d=$(mktemp -d) || exit 1
	trap 'rm -rf "$d"' EXIT
	mkdir -p "$d/go/internal/guard"
	printf '%s\n' 'module selftest' '' 'go 1.22' > "$d/go/go.mod"
	printf '%s\n' 'package guard' '' 'func Allow() bool { return true }' > "$d/go/internal/guard/g.go"
	printf '%s\n' 'package guard' '' 'import "testing"' '' \
		'func TestAllow(t *testing.T) {' '	if !Allow() {' '		t.Fatal("Allow() = false, want true")' '	}' '}' \
		> "$d/go/internal/guard/g_test.go"
	printf '%s\n' '--- a/go/internal/guard/g.go' '+++ b/go/internal/guard/g.go' '@@ -1,3 +1,3 @@' ' package guard' ' ' \
		'-func Allow() bool { return true }' '+func Allow() bool { return false }' > "$d/break.patch"
	printf '%s\n' '--- a/go/internal/guard/g.go' '+++ b/go/internal/guard/g.go' '@@ -1,3 +1,4 @@' ' package guard' ' ' \
		'+// Allow reports whether the call is allowed.' ' func Allow() bool { return true }' > "$d/comment.patch"
	if ! (check "$d" "$d/break.patch" '^TestAllow$') > "$d/break.log" 2>&1; then
		cat "$d/break.log"; echo "self-test failed: break.patch was not caught"; exit 1
	fi
	if (check "$d" "$d/comment.patch" '^TestAllow$') > "$d/comment.log" 2>&1; then
		cat "$d/comment.log"; echo "self-test failed: comment.patch counted as caught"; exit 1
	fi
	echo "self-test ok"; exit 0
}

if [ "${1:-}" = --self-test ] && [ $# -eq 1 ]; then
	self_test
fi
if [ $# -ne 2 ]; then
	echo "usage: $0 PATCH TESTREGEX | $0 --self-test" >&2; exit 2
fi
if [ ! -r "$1" ] || [ -d "$1" ]; then
	echo "patch not found: $1"; exit 1
fi
root=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel) || { echo "not in a git work tree" >&2; exit 2; }
check "$root" "$1" "$2"
