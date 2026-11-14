#!/usr/bin/env bash
# release.sh - build an iOS app, upload it to TestFlight and hand it to the tester groups.
# Runs on macOS stock bash 3.2 and on Linux bash. JSON, credentials, redaction and the ASC calls live in the Node
# helpers next to this file (config.mjs, creds.mjs, redact.mjs, summary.mjs, asc.mjs, what-to-test.mjs); this file
# only sequences them. Project values come from .hivemill/ios-release.json, credentials from the user's own setup.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RESET_PATH="/usr/bin:/bin:/usr/sbin:/sbin"
SIGNING_HINT="signing failed: in xcodeAccount mode sign in to Xcode (Settings > Accounts) with an account in the team; apiKey mode needs an Admin key (App Manager keys cannot cloud-sign); see README"

usage() {
  cat <<'EOF'
usage: release.sh [--config PATH] [--profile NAME] [--signing xcodeAccount|apiKey] [--dry-run] [--allow-dirty]
                  [--resume-build N] [--help]

  --config PATH        project config (default: <git top>/.hivemill/ios-release.json)
  --profile NAME       credential profile (default: ascProfile from the config, else "default")
  --signing MODE       xcodeAccount (default) signs with the Apple ID in Xcode; apiKey passes the key to xcodebuild
  --dry-run            run the checks and print what would happen; builds, uploads and writes nothing
  --allow-dirty        build a dirty checkout (only when worktreeBase is not set)
  --resume-build N     skip the build: wait for build N, then set What to Test and add the tester groups
  --help               this text

Exit codes: 0 ok | 2 usage | 3 config | 4 credentials | 5 preflight | 6 ASC API | 7 xcodebuild | 8 processing
            | 9 post-processing (rerun with --resume-build N).
The last stdout line is a one-line JSON summary; everything else goes to stderr.
EOF
}

usage_err() {
  printf 'release.sh: %s\n' "$1" >&2
  printf 'run release.sh --help for usage\n' >&2
  exit 2
}

# ---- flags ----
CONFIG_ARG=""
PROFILE_ARG=""
SIGNING_ARG=""
DRY=0
ALLOW_DIRTY=0
RESUME=""
while [ $# -gt 0 ]; do
  case "$1" in
    --help) usage; exit 0 ;;
    --dry-run) DRY=1; shift ;;
    --allow-dirty) ALLOW_DIRTY=1; shift ;;
    --config|--profile|--signing|--resume-build)
      [ $# -ge 2 ] || usage_err "$1 needs a value"
      case "$1" in
        --config) CONFIG_ARG="$2" ;;
        --profile) PROFILE_ARG="$2" ;;
        --signing) SIGNING_ARG="$2" ;;
        --resume-build) RESUME="$2" ;;
      esac
      shift 2 ;;
    *) usage_err "unknown flag: $1" ;;
  esac
done
case "$SIGNING_ARG" in
  ""|xcodeAccount|apiKey) ;;
  *) usage_err "--signing must be xcodeAccount or apiKey" ;;
esac
if [ -n "$RESUME" ]; then
  case "$RESUME" in
    *[!0-9]*) usage_err "--resume-build needs a positive integer" ;;
  esac
  [ "$RESUME" -gt 0 ] || usage_err "--resume-build needs a positive integer"
  [ "$DRY" = 0 ] || usage_err "--dry-run and --resume-build cannot be combined"
fi
if [ -n "$PROFILE_ARG" ]; then
  case "$PROFILE_ARG" in
    */*|*..*|.*|*[!A-Za-z0-9._-]*) usage_err "bad --profile name: use A-Z a-z 0-9 . _ - (no '/', no '..', no leading dot)" ;;
  esac
fi

# ---- state for the summary, cleanup and redaction ----
MODE_NAME=release
[ "$DRY" = 0 ] || MODE_NAME=dry-run
[ -z "$RESUME" ] || MODE_NAME=resume
TMP=""
WT=""
REPO_TOP=""
ERR_MSG=""
SIGNING=""
VERSION=""
BUILD=""
BUILD_ID=""
COMMIT=""
REDACT_VALUES=""
CRED_ID=""
CRED_ISSUER=""
CRED_PATH=""

redact_stream() { HIVEMILL_REDACT_VALUES="$REDACT_VALUES" node "$HERE/redact.mjs"; }
redact_file() { if [ -s "$1" ]; then redact_stream < "$1" >&2; fi; }
# log: our own static text. logr: anything that may contain external text or credential values.
log() { printf '%s\n' "$*" >&2; }
logr() { printf '%s\n' "$*" | redact_stream >&2; }

emit_summary() {
  local rc="$1" ok=false
  [ "$rc" -ne 0 ] || ok=true
  if [ -n "$TMP" ] && command -v node >/dev/null 2>&1; then
    printf '%s' "$ERR_MSG" > "$TMP/error"
    if HIVEMILL_REDACT_VALUES="$REDACT_VALUES" node "$HERE/summary.mjs" --ok "$ok" --exit-code "$rc" --mode "$MODE_NAME" \
      --signing "$SIGNING" --marketing-version "$VERSION" --build "$BUILD" --build-id "$BUILD_ID" --commit "$COMMIT" \
      --error-file "$TMP/error" --groups-file "$TMP/groups"; then
      return 0
    fi
  fi
  printf '{"ok":%s,"exitCode":%s,"mode":"%s","signing":null,"marketingVersion":null,"build":null,"buildId":null,"groups":[],"commit":null,"error":null}\n' "$ok" "$rc" "$MODE_NAME"
}

on_exit() {
  local rc=$?
  trap - EXIT
  set +e
  emit_summary "$rc"
  if [ -n "$WT" ] && [ -n "$REPO_TOP" ]; then
    git -C "$REPO_TOP" worktree remove --force "$WT" >/dev/null 2>&1
    git -C "$REPO_TOP" worktree prune >/dev/null 2>&1
  fi
  if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
  exit "$rc"
}

fail() { # fail CODE MESSAGE
  ERR_MSG="$2"
  logr "release.sh: $2"
  exit "$1"
}

TMP="$(mktemp -d)"
trap on_exit EXIT
trap 'exit 1' INT TERM HUP
: > "$TMP/groups"

# ---- helper wrappers ----
read_list() { # read_list FILE: NUL-terminated items into the array LIST
  LIST=()
  local item
  while IFS= read -r -d '' item; do LIST+=("$item"); done < "$1"
}
cfg() { node "$HERE/config.mjs" get --config "$CONFIG" "$1"; }
cfg_list() { cfg "$1" > "$TMP/list"; read_list "$TMP/list"; }

# asc SUBCOMMAND ...: runs asc.mjs with the credentials as per-command env only; stderr goes through redact.
asc() {
  local rc=0
  ASC_KEY_ID="$CRED_ID" ASC_ISSUER_ID="$CRED_ISSUER" ASC_KEY_PATH="$CRED_PATH" node "$HERE/asc.mjs" "$@" 2> "$TMP/asc.err" || rc=$?
  redact_file "$TMP/asc.err"
  return "$rc"
}

# run_streamed OUTFILE DIR CMD...: runs CMD in DIR, output through redact to stderr and OUTFILE; returns CMD's status.
run_streamed() {
  local out="$1" dir="$2"
  shift 2
  ( set +e; cd "$dir" || { echo 1 > "$TMP/rc"; exit 0; }; "$@"; echo $? > "$TMP/rc" ) 2>&1 | redact_stream | tee "$out" >&2
  return "$(cat "$TMP/rc")"
}

# ---- step 1: tools, config, credentials ----
if ! command -v node >/dev/null 2>&1; then
  ERR_MSG="node (>= 20) is required"
  printf 'release.sh: %s\n' "$ERR_MSG" >&2
  exit 5
fi
NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]')"
case "$NODE_MAJOR" in
  *[!0-9]*|"") fail 5 "cannot determine the node version; node >= 20 is required" ;;
esac
[ "$NODE_MAJOR" -ge 20 ] || fail 5 "node >= 20 is required (found $NODE_MAJOR)"

REPO_TOP="$(git rev-parse --show-toplevel 2>/dev/null)" || REPO_TOP=""
if [ -n "$CONFIG_ARG" ]; then
  CONFIG="$CONFIG_ARG"
elif [ -n "$REPO_TOP" ]; then
  CONFIG="$REPO_TOP/.hivemill/ios-release.json"
else
  fail 3 "no --config given and the current directory is not in a git checkout"
fi
case "$CONFIG" in
  /*) ;;
  *) CONFIG="$(pwd)/$CONFIG" ;;
esac
node "$HERE/config.mjs" check --config "$CONFIG" 2> "$TMP/cfg.err" || {
  rc=$?
  redact_file "$TMP/cfg.err"
  ERR_MSG="$(head -n 1 "$TMP/cfg.err")"
  exit "$rc"
}
[ -n "$REPO_TOP" ] || fail 5 "run release.sh inside the project's git checkout"

SIGNING="$SIGNING_ARG"
[ -n "$SIGNING" ] || SIGNING="$(cfg signing)"
PROFILE="$PROFILE_ARG"
[ -n "$PROFILE" ] || PROFILE="$(cfg ascProfile)"

rc=0
node "$HERE/creds.mjs" resolve --profile "$PROFILE" > "$TMP/creds" 2> "$TMP/creds.err" || rc=$?
if [ "$rc" -ne 0 ]; then
  redact_file "$TMP/creds.err"
  ERR_MSG="$(head -n 1 "$TMP/creds.err")"
  exit "$rc"
fi
redact_file "$TMP/creds.err"
{ IFS= read -r -d '' CRED_ID; IFS= read -r -d '' CRED_ISSUER; IFS= read -r -d '' CRED_PATH; } < "$TMP/creds"
REDACT_VALUES="$CRED_ID
$CRED_ISSUER
$CRED_PATH"
# The credentials are only ever passed as per-command prefixes (asc) or as xcodebuild arguments in apiKey mode.
unset ASC_KEY_ID ASC_ISSUER_ID ASC_KEY_PATH

SUBMIT_EXT="$(cfg submitExternal)"
cfg_list testerGroup.selectors
SEL=("${LIST[@]}")
cfg_list heavySlotCommand
HEAVY=()
if [ ${#LIST[@]} -gt 0 ]; then HEAVY=("${LIST[@]}"); fi

# ---- step 2: commit, build directory, preflight checks ----
BRANCH_REF="$(cfg defaultBranch)"
if [ -n "$BRANCH_REF" ]; then
  case "$BRANCH_REF" in
    */*)
      REMOTE="${BRANCH_REF%%/*}"
      if git -C "$REPO_TOP" remote | grep -qxF -- "$REMOTE"; then
        git -C "$REPO_TOP" fetch --quiet "$REMOTE" || fail 5 "git fetch $REMOTE failed"
      fi ;;
  esac
  COMMIT="$(git -C "$REPO_TOP" rev-parse --verify --quiet "$BRANCH_REF^{commit}")" || fail 5 "defaultBranch $BRANCH_REF does not resolve to a commit"
else
  COMMIT="$(git -C "$REPO_TOP" rev-parse --verify --quiet 'HEAD^{commit}')" || fail 5 "cannot resolve HEAD"
fi

WT_BASE="$(cfg worktreeBase)"
if [ -n "$WT_BASE" ]; then
  mkdir -p "$WT_BASE"
  WT="$WT_BASE/hivemill-ios-release-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  git -C "$REPO_TOP" worktree add --quiet --detach "$WT" "$COMMIT" >/dev/null 2>&1 || { WT=""; fail 5 "git worktree add failed under $WT_BASE"; }
  BUILD_DIR="$WT"
else
  BUILD_DIR="$REPO_TOP"
  if [ -n "$BRANCH_REF" ] && [ "$(git -C "$REPO_TOP" rev-parse HEAD)" != "$COMMIT" ]; then
    fail 5 "checkout is not at $BRANCH_REF; check it out or set worktreeBase"
  fi
  if [ "$ALLOW_DIRTY" = 0 ] && [ -n "$(git -C "$REPO_TOP" status --porcelain)" ]; then
    fail 5 "the working tree is dirty; commit or stash, or pass --allow-dirty"
  fi
fi
cd "$BUILD_DIR"
node "$HERE/config.mjs" check-forbidden --config "$CONFIG" --build-dir "$BUILD_DIR" 2> "$TMP/forb.err" || {
  rc=$?
  redact_file "$TMP/forb.err"
  ERR_MSG="$(head -n 1 "$TMP/forb.err")"
  exit "$rc"
}

# ---- xcodebuild (resolved once, under the inherited PATH) ----
XCODEBUILD=""
if [ -z "$RESUME" ]; then
  XCODEBUILD="$(command -v xcodebuild)" || fail 5 "xcodebuild not found on PATH (install Xcode)"
  : > "$TMP/xcv"
  run_streamed "$TMP/xcv" "$BUILD_DIR" env "PATH=$RESET_PATH" "$XCODEBUILD" -version || fail 5 "xcodebuild -version failed"
fi

WORKSPACE="$(cfg workspace)"
PROJECT="$(cfg project)"
CONFIGURATION="$(cfg configuration)"
SCHEME="$(cfg scheme)"
TARGET_ARGS=()
if [ -n "$WORKSPACE" ]; then TARGET_ARGS=(-workspace "$WORKSPACE"); else TARGET_ARGS=(-project "$PROJECT"); fi

# ---- step 3: marketing version, ASC lookups, notes, buildReadyCheck ----
MV_FORM="$(cfg marketingVersion.form)"
: > "$TMP/bs.txt"
if [ "$MV_FORM" = buildSetting ]; then
  if [ -z "$XCODEBUILD" ]; then
    XCODEBUILD="$(command -v xcodebuild)" || fail 5 "xcodebuild not found on PATH (needed for marketingVersion.buildSetting)"
  fi
  run_capture_rc=0
  env "PATH=$RESET_PATH" "$XCODEBUILD" -showBuildSettings "${TARGET_ARGS[@]}" -scheme "$SCHEME" -configuration "$CONFIGURATION" > "$TMP/bs.txt" 2> "$TMP/bs.err" || run_capture_rc=$?
  redact_file "$TMP/bs.err"
  [ "$run_capture_rc" -eq 0 ] || fail 5 "xcodebuild -showBuildSettings failed"
fi
rc=0
VERSION="$(node "$HERE/config.mjs" marketing-version --config "$CONFIG" --build-dir "$BUILD_DIR" < "$TMP/bs.txt" 2> "$TMP/mv.err")" || rc=$?
if [ "$rc" -ne 0 ]; then
  redact_file "$TMP/mv.err"
  fail "$rc" "$(head -n 1 "$TMP/mv.err")"
fi

BUNDLE_ID="$(cfg bundleId)"
APP_ID="$(asc app-id --bundle-id "$BUNDLE_ID")" || fail "$?" "could not look up the app for the bundle id"
if [ -n "$RESUME" ]; then
  BUILD="$RESUME"
else
  BUILD="$(asc next-build --app-id "$APP_ID" --version "$VERSION")" || fail "$?" "could not compute the next build number"
fi

TG_NAME=()
TG_ID=()
TG_KIND=()
TG_SCOPE=()
i=0
n=0
while [ "$n" -lt ${#SEL[@]} ]; do
  s_kind="${SEL[$n]}"
  s_val="${SEL[$((n + 1))]}"
  s_int="${SEL[$((n + 2))]}"
  case "$s_kind" in
    id) GI_ARGS=(--id "$s_val") ;;
    name-internal) GI_ARGS=(--name "$s_val" --internal "$s_int") ;;
    *) GI_ARGS=(--name "$s_val") ;;
  esac
  rc=0
  line="$(asc group-info --app-id "$APP_ID" "${GI_ARGS[@]}")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    # asc() already echoed its (redacted) stderr; keep its first line in the message and summary.
    gi_err="$(head -n 1 "$TMP/asc.err")"
    fail "$rc" "beta group lookup failed for testerGroup entry $i: ${gi_err#asc: }"
  fi
  g_name=""
  IFS=$'\t' read -r g_id g_kind g_scope g_name <<EOF
$line
EOF
  [ -n "$g_name" ] || g_name="$s_val"
  j=0
  while [ "$j" -lt "$i" ]; do
    if [ "${TG_ID[$j]}" = "$g_id" ]; then fail 6 "testerGroup entries $j and $i are the same group $g_id"; fi
    j=$((j + 1))
  done
  TG_NAME+=("$g_name")
  TG_ID+=("$g_id")
  TG_KIND+=("$g_kind")
  TG_SCOPE+=("$g_scope")
  printf '%s\0%s\0' "$g_name" pending >> "$TMP/groups"
  i=$((i + 1))
  n=$((n + 3))
done

NOTES_KIND="$(cfg notes.kind)"
if [ "$NOTES_KIND" = path ]; then
  NOTES_SRC="$(node "$HERE/config.mjs" notes-path --config "$CONFIG" --build "$BUILD" --build-dir "$BUILD_DIR")"
  [ -f "$NOTES_SRC" ] || fail 5 "the notes file does not exist: $NOTES_SRC"
  cp "$NOTES_SRC" "$TMP/notes.raw"
else
  cfg_list notes.command
  run_rc=0
  ( cd "$BUILD_DIR" && "${LIST[@]}" ) > "$TMP/notes.raw" 2> "$TMP/notes.err" || run_rc=$?
  redact_file "$TMP/notes.err"
  [ "$run_rc" -eq 0 ] || fail 5 "the notes command failed (exit $run_rc)"
fi
rc=0
node "$HERE/what-to-test.mjs" < "$TMP/notes.raw" > "$TMP/notes.txt" 2> "$TMP/notes.err" || rc=$?
if [ "$rc" -ne 0 ]; then
  redact_file "$TMP/notes.err"
  fail 5 "the What to Test text is not usable: $(head -n 1 "$TMP/notes.err")"
fi
NOTES_LEN="$(wc -c < "$TMP/notes.txt" | tr -d ' ')"

if [ -z "$RESUME" ]; then
  cfg_list buildReadyCheck
  if [ ${#LIST[@]} -gt 0 ]; then
    CHECK_CMD=("${LIST[@]}" "$COMMIT")
    logr "buildReadyCheck: ${CHECK_CMD[*]}"
    rc=0
    run_streamed "$TMP/check.log" "$BUILD_DIR" "${CHECK_CMD[@]}" || rc=$?
    [ "$rc" -eq 0 ] || fail 5 "buildReadyCheck failed (exit $rc)"
  fi
fi

# ---- the xcodebuild command lines (built once; used by the dry run and the real run) ----
AUTH_ARGS=()
if [ "$SIGNING" = apiKey ]; then
  AUTH_ARGS=(-authenticationKeyPath "$CRED_PATH" -authenticationKeyID "$CRED_ID" -authenticationKeyIssuerID "$CRED_ISSUER")
fi
ARCHIVE_PATH="$TMP/App.xcarchive"
EXPORT_PATH="$TMP/export"
PLIST="$TMP/ExportOptions.plist"
TEAM_ID="$(cfg teamId)"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>method</key><string>app-store-connect</string>
  <key>destination</key><string>upload</string>
  <key>signingStyle</key><string>automatic</string>
  <key>manageAppVersionAndBuildNumber</key><false/>
  <key>teamID</key><string>$TEAM_ID</string>
</dict>
</plist>
EOF
XB_PREFIX=()
if [ ${#HEAVY[@]} -gt 0 ]; then XB_PREFIX=("${HEAVY[@]}"); fi
XB_PREFIX+=(env "PATH=$RESET_PATH" "${XCODEBUILD:-xcodebuild}")
ARCHIVE_CMD=("${XB_PREFIX[@]}" archive "${TARGET_ARGS[@]}" -scheme "$SCHEME" -configuration "$CONFIGURATION"
  -destination generic/platform=iOS -archivePath "$ARCHIVE_PATH" -allowProvisioningUpdates "CURRENT_PROJECT_VERSION=$BUILD")
EXPORT_CMD=("${XB_PREFIX[@]}" -exportArchive -archivePath "$ARCHIVE_PATH" -exportPath "$EXPORT_PATH"
  -exportOptionsPlist "$PLIST" -allowProvisioningUpdates)
if [ ${#AUTH_ARGS[@]} -gt 0 ]; then
  ARCHIVE_CMD+=("${AUTH_ARGS[@]}")
  EXPORT_CMD+=("${AUTH_ARGS[@]}")
fi

group_decision() { # group_decision KIND SCOPE -> skipped-all-builds | skipped-external | added
  if [ "$1" = internal ] && [ "$2" = all ]; then echo skipped-all-builds
  elif [ "$1" != internal ] && [ "$SUBMIT_EXT" != true ]; then echo skipped-external
  else echo added
  fi
}

# ---- dry run: report and stop ----
if [ "$DRY" = 1 ]; then
  log "dry run: nothing is built, uploaded or written to App Store Connect"
  logr "commit: $COMMIT"
  logr "marketing version: $VERSION"
  logr "next build: $BUILD"
  logr "signing: $SIGNING"
  log "notes: $NOTES_LEN characters"
  : > "$TMP/groups"
  i=0
  while [ "$i" -lt ${#TG_NAME[@]} ]; do
    d="$(group_decision "${TG_KIND[$i]}" "${TG_SCOPE[$i]}")"
    case "$d" in
      added) txt="would add"; st=would-add ;;
      skipped-all-builds) txt="would skip (gets all builds)"; st=would-skip ;;
      *) txt="would skip (external; submitExternal false)"; st=would-skip ;;
    esac
    logr "${TG_NAME[$i]}: $txt"
    printf '%s\0%s\0' "${TG_NAME[$i]}" "$st" >> "$TMP/groups"
    i=$((i + 1))
  done
  n="$(cfg gate-count)"
  i=0
  while [ "$i" -lt "$n" ]; do
    node "$HERE/config.mjs" get-gate --config "$CONFIG" --index "$i" > "$TMP/gate"
    read_list "$TMP/gate"
    logr "gate $i (cwd ${LIST[0]}): ${LIST[*]:1}"
    i=$((i + 1))
  done
  logr "archive: ${ARCHIVE_CMD[*]}"
  logr "export: ${EXPORT_CMD[*]}"
  log "ExportOptions.plist:"
  redact_file "$PLIST"
  exit 0
fi

# ---- build and upload ----
if [ -z "$RESUME" ]; then
  # step 4: gates, in order, each in its cwd
  BUILD_REAL="$(cd "$BUILD_DIR" && pwd -P)"
  n="$(cfg gate-count)"
  i=0
  while [ "$i" -lt "$n" ]; do
    node "$HERE/config.mjs" get-gate --config "$CONFIG" --index "$i" > "$TMP/gate"
    read_list "$TMP/gate"
    gdir="$BUILD_DIR/${LIST[0]}"
    [ -d "$gdir" ] || fail 5 "gate $i: directory ${LIST[0]} does not exist"
    greal="$(cd "$gdir" && pwd -P)"
    case "$greal/" in
      "$BUILD_REAL"/*) ;;
      *) fail 5 "gate $i: ${LIST[0]} resolves outside the build directory" ;;
    esac
    GATE_CMD=()
    if [ ${#HEAVY[@]} -gt 0 ]; then GATE_CMD=("${HEAVY[@]}"); fi
    GATE_CMD+=("${LIST[@]:1}")
    logr "gate $i: ${GATE_CMD[*]}"
    rc=0
    run_streamed "$TMP/gate.log" "$gdir" "${GATE_CMD[@]}" || rc=$?
    [ "$rc" -eq 0 ] || fail 5 "gate $i failed (exit $rc)"
    i=$((i + 1))
  done

  # step 5: archive
  logr "archive: ${ARCHIVE_CMD[*]}"
  rc=0
  run_streamed "$TMP/archive.log" "$BUILD_DIR" "${ARCHIVE_CMD[@]}" || rc=$?
  [ "$rc" -eq 0 ] || fail 7 "xcodebuild archive failed (exit $rc)"

  # step 6: export and upload
  logr "export: ${EXPORT_CMD[*]}"
  rc=0
  run_streamed "$TMP/export.log" "$BUILD_DIR" "${EXPORT_CMD[@]}" || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -qE 'Cloud signing permission error|No signing certificate' "$TMP/export.log"; then log "$SIGNING_HINT"; fi
    fail 7 "xcodebuild -exportArchive failed (exit $rc)"
  fi
fi

# step 7: wait for processing
POLL_MIN="$(cfg pollTimeoutMinutes)"
POLL_SEC="$(cfg pollIntervalSeconds)"
log "waiting for build $BUILD to finish processing"
BUILD_ID="$(asc wait-valid --app-id "$APP_ID" --version "$VERSION" --build "$BUILD" --timeout-min "$POLL_MIN" --interval-s "$POLL_SEC")" || fail "$?" "build $BUILD did not become VALID"

# step 8: What to Test
asc set-whats-new --build-id "$BUILD_ID" --locale "$(cfg notesLocale)" --notes-file "$TMP/notes.txt" || fail 9 "setting What to Test failed; rerun with --resume-build $BUILD"

# step 9: tester groups, in config order
i=0
while [ "$i" -lt ${#TG_NAME[@]} ]; do
  ADD_ARGS=(add-to-group --build-id "$BUILD_ID" --group-id "${TG_ID[$i]}")
  if [ "$SUBMIT_EXT" = true ]; then ADD_ARGS+=(--submit-external); fi
  status="$(asc "${ADD_ARGS[@]}")" || {
    printf '%s\0%s\0' "${TG_NAME[$i]}" failed >> "$TMP/groups"
    fail 9 "adding the build to group ${TG_NAME[$i]} failed; rerun with --resume-build $BUILD"
  }
  printf '%s\0%s\0' "${TG_NAME[$i]}" "$status" >> "$TMP/groups"
  logr "group ${TG_NAME[$i]}: $status"
  i=$((i + 1))
done

# step 10: the summary line is printed by the exit trap
log "done: build $BUILD of $VERSION is on TestFlight"
exit 0
