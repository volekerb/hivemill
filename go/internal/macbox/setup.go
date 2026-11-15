package macbox

import (
	"errors"
	"fmt"
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

var (
	userRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	// The launcher path lands in /etc/sudoers.d, where space , : = ! ( ) # are syntax.
	launcherRe = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+$`)
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
	if !userRe.MatchString(o.OwnerUser) {
		return o, fmt.Errorf("macbox setup: owner user %q is not a plain login name ([A-Za-z0-9._-])", o.OwnerUser)
	}
	for _, p := range []struct{ n, v string }{{"owner home", o.OwnerHome}, {"factory home", o.FactoryHome}, {"launcher path", o.LauncherPath}} {
		if err := checkAbs(p.n, p.v); err != nil {
			return o, err
		}
	}
	if !launcherRe.MatchString(o.LauncherPath) || strings.HasSuffix(o.LauncherPath, "/") {
		return o, errors.New("macbox setup: launcher path may only use letters, digits and . _ / + - (it is written into sudoers)")
	}
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

// SetupScript returns the owner setup script (POSIX sh).
func SetupScript(opts SetupOpts) (string, error) {
	o, err := opts.normalized()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, preamble, "setup.sh <path-of-macbox-launch-binary>", shq(o.OwnerUser), o.OwnerUser)
	fmt.Fprintf(&b, `
LAUNCHER_SRC=${1:-}
if [ -z "$LAUNCHER_SRC" ] || [ ! -f "$LAUNCHER_SRC" ]; then
  echo "usage: sudo sh setup.sh <path of the macbox-launch binary>" >&2
  exit 1
fi

free_id() { # $1 = dscl list path, $2 = key: print the highest unused id below 500
  used=$(dscl . -list "$1" "$2" | awk '{print $2}')
  n=499
  while [ "$n" -ge 200 ]; do
    if ! printf '%%s\n' "$used" | grep -qx "$n"; then echo "$n"; return 0; fi
    n=$((n - 1))
  done
  echo "no free id between 200 and 499" >&2
  exit 1
}

mkuser() { # $1 = account name
  if dscl . -read "/Users/$1" >/dev/null 2>&1; then echo "  $1 exists, keeping"; return 0; fi
  uid=$(free_id /Users UniqueID)
  dscl . -create "/Users/$1"
  dscl . -create "/Users/$1" UniqueID "$uid"
  dscl . -create "/Users/$1" PrimaryGroupID "$GID"
  dscl . -create "/Users/$1" UserShell /usr/bin/false
  dscl . -create "/Users/$1" NFSHomeDirectory /var/empty
  dscl . -create "/Users/$1" RealName "Hivemill worker $1"
  dscl . -create "/Users/$1" IsHidden 1
  dscl . -create "/Users/$1" Password '*'
}

echo "step %[1]s: create group %[2]s and accounts %[3]s, %[4]s (dscl)"
if dscl . -read /Groups/%[2]s >/dev/null 2>&1; then
  echo "  group %[2]s exists, keeping"
else
  GID=$(free_id /Groups PrimaryGroupID)
  dscl . -create /Groups/%[2]s
  dscl . -create /Groups/%[2]s PrimaryGroupID "$GID"
  dscl . -create /Groups/%[2]s RealName "Hivemill workers"
fi
GID=$(dscl . -read /Groups/%[2]s PrimaryGroupID | awk '{print $2}')
mkuser %[3]s
mkuser %[4]s
dscl . -append /Groups/%[2]s GroupMembership %[3]s %[4]s

LAUNCHER=%[5]s
echo "step %[6]s: install the launcher as root:wheel 0755 at $LAUNCHER"
install -d -o root -g wheel -m 0755 "$(dirname "$(dirname "$LAUNCHER")")" "$(dirname "$LAUNCHER")"
install -o root -g wheel -m 0755 "$LAUNCHER_SRC" "$LAUNCHER"

echo "step %[7]s: write /etc/sudoers.d/hivemill (owner to worker only)"
TMP=$(mktemp /etc/sudoers.d/.hivemill.XXXXXX)
cat > "$TMP" <<'SUDOERS_EOF'
%[8]s
SUDOERS_EOF
chmod 0440 "$TMP"
chown root:wheel "$TMP"
if ! visudo -cf "$TMP"; then
  rm -f "$TMP"
  echo "sudoers file failed validation; nothing installed" >&2
  exit 1
fi
mv "$TMP" /etc/sudoers.d/hivemill

OWNER_HOME=%[9]s
FACTORY_HOME=%[10]s
echo "step %[11]s: ACLs for group %[2]s (home: traverse only; factory: read; factory/db: write)"
chmod +a "%[12]s" "$OWNER_HOME"
chmod -R +a "%[13]s" "$FACTORY_HOME"
mkdir -p "$FACTORY_HOME/db"
chmod -R +a "%[14]s" "$FACTORY_HOME/db"

echo "done. Verify with: sudo -u %[3]s -n %[5]s --help"
`, StepAccounts, GroupName, AccountRO, AccountRW, shq(o.LauncherPath), StepLauncher, StepSudoers,
		SudoersLine(o), shq(o.OwnerHome), shq(o.FactoryHome), StepACL, aclHome, aclRead, aclDB)
	return b.String(), nil
}

// UninstallScript returns the script that reverses every SetupScript step.
// Each undo is idempotent: a missing piece is reported and skipped.
func UninstallScript(opts SetupOpts) (string, error) {
	o, err := opts.normalized()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, preamble, "uninstall.sh", shq(o.OwnerUser), o.OwnerUser)
	fmt.Fprintf(&b, `
LAUNCHER=%[1]s
OWNER_HOME=%[2]s
FACTORY_HOME=%[3]s

echo "undo step %[4]s: remove the group ACL entries"
if [ -e "$OWNER_HOME" ]; then chmod -a "%[5]s" "$OWNER_HOME" || echo "  home entry already absent"; fi
if [ -d "$FACTORY_HOME/db" ]; then chmod -R -a "%[6]s" "$FACTORY_HOME/db" || echo "  db entries already absent"; fi
if [ -d "$FACTORY_HOME" ]; then chmod -R -a "%[7]s" "$FACTORY_HOME" || echo "  factory entries already absent"; fi

echo "undo step %[8]s: remove /etc/sudoers.d/hivemill"
rm -f /etc/sudoers.d/hivemill

echo "undo step %[9]s: remove the launcher"
rm -f "$LAUNCHER"
rmdir "$(dirname "$LAUNCHER")" 2>/dev/null || echo "  launcher directory not empty or gone, keeping"

echo "undo step %[10]s: delete accounts and group"
for u in %[11]s %[12]s; do
  if dscl . -read "/Users/$u" >/dev/null 2>&1; then dscl . -delete "/Users/$u"; else echo "  $u absent"; fi
done
if dscl . -read /Groups/%[13]s >/dev/null 2>&1; then dscl . -delete /Groups/%[13]s; else echo "  group %[13]s absent"; fi

echo "done."
`, shq(o.LauncherPath), shq(o.OwnerHome), shq(o.FactoryHome), StepACL, aclHome, aclDB, aclRead,
		StepSudoers, StepLauncher, StepAccounts, AccountRO, AccountRW, GroupName)
	return b.String(), nil
}
