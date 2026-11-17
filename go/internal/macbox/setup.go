package macbox

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Fixed account names of ADR 0003 section 3.2.
const (
	AccountRO = "_hivemill_ro"
	AccountRW = "_hivemill_rw"
	GroupName = "_hivemill"
	// DefaultLauncher is the root-owned launcher the sudoers rule names.
	DefaultLauncher = "/usr/local/libexec/hivemill/macbox-launch"
)

// SetupOpts are the values interpolated into the owner scripts.
type SetupOpts struct {
	OwnerUser    string // the owner's login name; the only user the sudoers rule names
	OwnerHome    string // the owner's home: workers may traverse (search) it only
	FactoryHome  string // readable by the group; FactoryHome/db is writable
	LauncherPath string // default DefaultLauncher
}

// LauncherDir is the only directory the launcher may live in: root-owned and
// outside every home, so the owner cannot swap the file the sudoers rule names.
const LauncherDir = "/usr/local/libexec/hivemill"

var (
	userRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	// The launcher name lands in /etc/sudoers.d, where space , : = ! ( ) # are syntax.
	launcherNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)
)

func checkValue(name, v string) error {
	if v == "" {
		return fmt.Errorf("macbox setup: %s is empty; pass it as a flag", name)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || r == '\'' || r == '"' || r == '\\' {
			return fmt.Errorf("macbox setup: %s contains a newline, control character, quote or backslash; use a plain path", name)
		}
	}
	return nil
}

func checkAbs(name, v string) error {
	if err := checkValue(name, v); err != nil {
		return err
	}
	if !strings.HasPrefix(v, "/") || strings.Contains(v, "/../") || strings.HasSuffix(v, "/..") {
		return fmt.Errorf("macbox setup: %s must be an absolute path without '..': %q", name, v)
	}
	return nil
}

func (o SetupOpts) normalized() (SetupOpts, error) {
	if o.LauncherPath == "" {
		o.LauncherPath = DefaultLauncher
	}
	if err := checkValue("owner user", o.OwnerUser); err != nil {
		return o, err
	}
	if !userRe.MatchString(o.OwnerUser) || strings.EqualFold(o.OwnerUser, "ALL") {
		return o, fmt.Errorf("macbox setup: owner user %q is not a plain login name ([A-Za-z0-9._-], not ALL)", o.OwnerUser)
	}
	for _, p := range []struct{ n, v string }{{"owner home", o.OwnerHome}, {"factory home", o.FactoryHome}, {"launcher path", o.LauncherPath}} {
		if err := checkAbs(p.n, p.v); err != nil {
			return o, err
		}
	}
	o.OwnerHome = path.Clean(o.OwnerHome)
	o.FactoryHome = path.Clean(o.FactoryHome)
	if o.OwnerHome == "/" {
		return o, errors.New("macbox setup: owner home must not be /; pass the owner's home, e.g. /Users/<owner>")
	}
	if !strings.HasPrefix(o.FactoryHome, o.OwnerHome+"/") {
		return o, fmt.Errorf("macbox setup: factory home %q must be strictly inside owner home %q (the group gets read on the whole factory tree)", o.FactoryHome, o.OwnerHome)
	}
	dir, name := path.Split(o.LauncherPath)
	if path.Clean(dir) != LauncherDir || !launcherNameRe.MatchString(name) {
		return o, fmt.Errorf("macbox setup: launcher path must be %s/<name> with a name of letters, digits and . _ + - (got %q)", LauncherDir, o.LauncherPath)
	}
	o.LauncherPath = LauncherDir + "/" + name
	return o, nil
}

// shq single-quotes v for sh; checkValue has already refused single quotes.
func shq(v string) string { return "'" + v + "'" }

// SudoersLine is the one rule of /etc/sudoers.d/hivemill: owner to worker only.
func SudoersLine(o SetupOpts) string {
	return fmt.Sprintf("%s ALL=(%s,%s) NOPASSWD:SETENV: %s", o.OwnerUser, AccountRO, AccountRW, o.LauncherPath)
}

// ACL entries. Home: traverse only. Tree: read. db: read and write.
const (
	aclHome = "group:" + GroupName + " allow search"
	aclRead = "group:" + GroupName + " allow list,search,readattr,readextattr,readsecurity,read,execute,file_inherit,directory_inherit"
	aclDB   = "group:" + GroupName + " allow add_file,add_subdirectory,delete_child,write,append,writeattr,writeextattr,file_inherit,directory_inherit"
)

// Step ids, shared by setup and uninstall.
const (
	StepAccounts = "accounts"
	StepLauncher = "launcher"
	StepSudoers  = "sudoers"
	StepACL      = "acl"
)

const preamble = `#!/bin/sh
# Hivemill macbox owner script (ADR 0003 section 3.2). Read it, then run it
# with: sudo sh %s
# It is printed by macbox-setup; the factory never runs it.
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root with sudo: sudo sh $0" >&2
  exit 1
fi
if [ "${SUDO_USER:-}" != %s ]; then
  echo "must be run with sudo by the owner %s (SUDO_USER=${SUDO_USER:-unset})" >&2
  exit 1
fi
`

// launcherAncestors are the directories above and including LauncherDir that
// the setup script checks (root-owned, not group/world writable) and creates
// only when absent. It never chowns or chmods a directory that exists.
var launcherAncestors = []string{"/usr", "/usr/local", "/usr/local/libexec", LauncherDir}

// vars returns the placeholder replacer shared by both scripts.
func vars(o SetupOpts) *strings.Replacer {
	return strings.NewReplacer(
		"@GROUP@", GroupName, "@RO@", AccountRO, "@RW@", AccountRW,
		"@LAUNCHER@", shq(o.LauncherPath), "@LAUNCHER_DIR@", shq(LauncherDir),
		"@ANCESTORS@", shqAll(launcherAncestors),
		"@OWNER_HOME@", shq(o.OwnerHome), "@FACTORY_HOME@", shq(o.FactoryHome),
		"@SUDOERS@", SudoersLine(o),
		"@ACL_HOME@", aclHome, "@ACL_READ@", aclRead, "@ACL_DB@", aclDB,
		"@S_ACCOUNTS@", StepAccounts, "@S_LAUNCHER@", StepLauncher, "@S_SUDOERS@", StepSudoers, "@S_ACL@", StepACL,
	)
}

func shqAll(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = shq(v)
	}
	return strings.Join(q, " ")
}

// resolvePaths resolves both homes with pwd -P and refuses a factory home
// that is not strictly inside the owner home (symlinks included).
const resolvePaths = `
HOME_REAL=$(cd "$OWNER_HOME" && pwd -P)
FACTORY_REAL=$(cd "$FACTORY_HOME" && pwd -P)
if [ "$HOME_REAL" = / ]; then
  echo "owner home resolves to /; refusing" >&2
  exit 1
fi
case "$FACTORY_REAL" in
  "$HOME_REAL"/?*) ;;
  *)
    echo "factory home resolves to $FACTORY_REAL, not strictly inside owner home $HOME_REAL; refusing" >&2
    exit 1
    ;;
esac
`

// resolvePathsUninstallACL resolves homes like resolvePaths but warns and sets
// ACL_OK=0 instead of exiting when the factory tree is not under the owner home.
const resolvePathsUninstallACL = `
HOME_REAL=$(cd "$OWNER_HOME" && pwd -P)
FACTORY_REAL=$(cd "$FACTORY_HOME" && pwd -P)
ACL_OK=1
if [ "$HOME_REAL" = / ]; then
  echo "owner home resolves to /; skipping ACL undo" >&2
  ACL_OK=0
else
  case "$FACTORY_REAL" in
    "$HOME_REAL"/?*) ;;
    *)
      echo "factory home resolves to $FACTORY_REAL, not strictly inside owner home $HOME_REAL; skipping ACL undo" >&2
      ACL_OK=0
      ;;
  esac
fi
`

const setupBody = `
LAUNCHER_SRC=${1:-}
if [ -z "$LAUNCHER_SRC" ] || [ ! -f "$LAUNCHER_SRC" ]; then
  echo "usage: sudo sh setup.sh <path of the macbox-launch binary>" >&2
  exit 1
fi
LAUNCHER=@LAUNCHER@
OWNER_HOME=@OWNER_HOME@
FACTORY_HOME=@FACTORY_HOME@

echo "precheck: paths (nothing is changed before every check passes)"
for d in "$OWNER_HOME" "$FACTORY_HOME"; do
  if [ ! -d "$d" ]; then echo "$d is not a directory; create it first" >&2; exit 1; fi
done
` + resolvePaths + `
root_dir_ok() { # $1 = dir: a real directory owned by root, not group or world writable
  [ -d "$1" ] && [ ! -L "$1" ] && [ -n "$(find "$1" -maxdepth 0 -user root ! -perm -0020 ! -perm -0002)" ]
}
for d in @ANCESTORS@; do
  if [ -e "$d" ] || [ -L "$d" ]; then
    if ! root_dir_ok "$d"; then
      echo "$d is not a root-owned directory closed to group and world writes; fix it or remove it, then rerun" >&2
      exit 1
    fi
  fi
done

free_id() { # $1 = dscl list path, $2 = key: print the highest unused id below 500
  used=$(dscl . -list "$1" "$2" | awk '{print $2}')
  n=499
  while [ "$n" -ge 200 ]; do
    if ! printf '%s\n' "$used" | grep -qx "$n"; then echo "$n"; return 0; fi
    n=$((n - 1))
  done
  echo "no free id between 200 and 499" >&2
  exit 1
}

mkuser() { # $1 = account name; completes a record a stopped run left half-built
  if dscl . -read "/Users/$1" UniqueID >/dev/null 2>&1; then
    echo "  $1 exists, keeping its UniqueID"
  else
    if ! dscl . -read "/Users/$1" >/dev/null 2>&1; then dscl . -create "/Users/$1"; fi
    uid=$(free_id /Users UniqueID)
    dscl . -create "/Users/$1" UniqueID "$uid"
  fi
  dscl . -create "/Users/$1" PrimaryGroupID "$GID"
  dscl . -create "/Users/$1" UserShell /usr/bin/false
  dscl . -create "/Users/$1" NFSHomeDirectory /var/empty
  dscl . -create "/Users/$1" RealName "Hivemill worker $1"
  dscl . -create "/Users/$1" IsHidden 1
  dscl . -create "/Users/$1" Password '*'
}

echo "step @S_ACCOUNTS@: create group @GROUP@ and accounts @RO@, @RW@ (dscl)"
if dscl . -read /Groups/@GROUP@ PrimaryGroupID >/dev/null 2>&1; then
  echo "  group @GROUP@ exists, keeping"
else
  if ! dscl . -read /Groups/@GROUP@ >/dev/null 2>&1; then dscl . -create /Groups/@GROUP@; fi
  GID=$(free_id /Groups PrimaryGroupID)
  dscl . -create /Groups/@GROUP@ PrimaryGroupID "$GID"
fi
dscl . -create /Groups/@GROUP@ RealName "Hivemill workers"
GID=$(dscl . -read /Groups/@GROUP@ PrimaryGroupID | awk '{print $2}')
mkuser @RO@
mkuser @RW@
for u in @RO@ @RW@; do
  dseditgroup -o edit -a "$u" -t user @GROUP@
done

echo "step @S_LAUNCHER@: install the launcher as root:wheel 0755 at $LAUNCHER"
for d in @ANCESTORS@; do
  if [ ! -e "$d" ]; then
    mkdir "$d"
    chown root:wheel "$d"
    chmod 0755 "$d"
    echo "  created $d"
  fi
done
install -o root -g wheel -m 0755 "$LAUNCHER_SRC" "$LAUNCHER"

echo "step @S_SUDOERS@: write /etc/sudoers.d/hivemill (owner to worker only)"
TMP=$(mktemp /etc/sudoers.d/.hivemill.XXXXXX)
cat > "$TMP" <<'SUDOERS_EOF'
@SUDOERS@
SUDOERS_EOF
chmod 0440 "$TMP"
chown root:wheel "$TMP"
if ! visudo -cf "$TMP"; then
  rm -f "$TMP"
  echo "sudoers file failed validation; nothing installed" >&2
  exit 1
fi
mv "$TMP" /etc/sudoers.d/hivemill

echo "step @S_ACL@: ACLs for group @GROUP@ (home and dirs down to the factory: traverse only; factory: read; factory/db: write)"
chmod +a "@ACL_HOME@" "$HOME_REAL"
d=$(dirname "$FACTORY_REAL")
while [ "$d" != "$HOME_REAL" ] && [ "$d" != / ]; do
  chmod +a "@ACL_HOME@" "$d"
  d=$(dirname "$d")
done
chmod -R +a "@ACL_READ@" "$FACTORY_REAL"
mkdir -p "$FACTORY_REAL/db"
chmod -R +a "@ACL_DB@" "$FACTORY_REAL/db"

echo "done. Verify with: sudo -u @RO@ -n @LAUNCHER@ --help"
`

const uninstallBody = `
LAUNCHER=@LAUNCHER@
LAUNCHER_DIR=@LAUNCHER_DIR@
OWNER_HOME=@OWNER_HOME@
FACTORY_HOME=@FACTORY_HOME@

echo "undo step @S_ACL@: remove the group ACL entries"
if [ -d "$OWNER_HOME" ] && [ -d "$FACTORY_HOME" ]; then
` + resolvePathsUninstallACL + `
  if [ "$ACL_OK" -eq 1 ]; then
  if [ -d "$FACTORY_REAL/db" ]; then chmod -R -a "@ACL_DB@" "$FACTORY_REAL/db" || echo "  db entries already absent"; fi
  chmod -R -a "@ACL_READ@" "$FACTORY_REAL" || echo "  factory entries already absent"
  d=$(dirname "$FACTORY_REAL")
  while [ "$d" != "$HOME_REAL" ] && [ "$d" != / ]; do
    chmod -a "@ACL_HOME@" "$d" || echo "  $d entry already absent"
    d=$(dirname "$d")
  done
  chmod -a "@ACL_HOME@" "$HOME_REAL" || echo "  home entry already absent"
  fi
else
  echo "  owner or factory home missing, skipping"
fi

echo "undo step @S_SUDOERS@: remove /etc/sudoers.d/hivemill"
rm -f /etc/sudoers.d/hivemill

echo "undo step @S_LAUNCHER@: remove the launcher (parent directories above $LAUNCHER_DIR are kept)"
rm -f "$LAUNCHER"
if [ -d "$LAUNCHER_DIR" ]; then rmdir "$LAUNCHER_DIR" || echo "  $LAUNCHER_DIR not empty, keeping"; fi

echo "undo step @S_ACCOUNTS@: delete accounts and group"
for u in @RO@ @RW@; do
  if dscl . -read "/Users/$u" >/dev/null 2>&1; then dscl . -delete "/Users/$u"; else echo "  $u absent"; fi
done
if dscl . -read /Groups/@GROUP@ >/dev/null 2>&1; then dscl . -delete /Groups/@GROUP@; else echo "  group @GROUP@ absent"; fi

echo "done."
`

// SetupScript returns the owner setup script (POSIX sh).
func SetupScript(opts SetupOpts) (string, error) {
	o, err := opts.normalized()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(preamble, "setup.sh <path-of-macbox-launch-binary>", shq(o.OwnerUser), o.OwnerUser) +
		vars(o).Replace(setupBody), nil
}

// UninstallScript returns the script that reverses every SetupScript step.
// Each undo is idempotent: a missing piece is reported and skipped.
func UninstallScript(opts SetupOpts) (string, error) {
	o, err := opts.normalized()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(preamble, "uninstall.sh", shq(o.OwnerUser), o.OwnerUser) +
		vars(o).Replace(uninstallBody), nil
}
