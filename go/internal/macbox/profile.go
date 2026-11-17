// Package macbox generates the macOS Seatbelt (SBPL) profile of the worker
// sandbox (ADR 0003 sections 3.1 and 3.2, stage M4a). It is pure string
// generation: nothing here runs sandbox-exec or touches the file system, so it
// builds and is tested on Linux. Real behaviour is proven by the Mac runs.
package macbox

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Role is the worker role a profile is generated for.
type Role string

// Roles of ADR 0003 section 3.2.
const (
	RoleReviewer Role = "reviewer"
	RoleQA       Role = "qa"
	RoleCoder    Role = "coder"
	RoleGate     Role = "gate"
)

// ProfileInput is everything a profile is built from.
type ProfileInput struct {
	Role          Role
	OwnerHome     string   // the owner's home: secret subdirectories are denied
	FactoryHome   string   // readable; FactoryHome/db is writable
	AgentDir      string   // worker HOME, CLAUDE_CONFIG_DIR and TMPDIR live here (read-write)
	CloneDir      string   // coder clone, qa clone or gate tree
	CloneWritable bool     // CloneDir read-write when true, read-only otherwise
	ReadOnlyPaths []string // Claude install, Go toolchain, extra system paths
	Network       bool     // network-outbound allowed
}

// systemReadPaths are subpaths every role may read.
var systemReadPaths = []string{"/usr", "/bin", "/sbin", "/System", "/Library", "/private/etc"}

// systemReadLiterals are single files or device nodes every role may read.
var systemReadLiterals = []string{"/dev/null", "/dev/urandom", "/dev/random"}

// ownerSecretDirs are denied below OwnerHome, after every allow rule.
var ownerSecretDirs = []string{
	".ssh",
	".claude",
	".config/gh",
	".config/factory",
	"Library/Keychains",
}

// machServices is the complete list of Mach services a worker may look up.
// Anything not listed is denied by default. Never add a Keychain, launchd
// owner-domain, Apple Events or pasteboard service here (a test asserts it).
var machServices = []string{
	"com.apple.system.opendirectoryd.libinfo", // getpwuid/getpwnam (git, ssh-less tools need the user name)
	"com.apple.system.notification_center",    // libnotify, loaded by libSystem consumers
	"com.apple.logd",                          // os_log, so tools can log without failing
	"com.apple.cfprefsd.daemon",               // system-wide preferences read by CoreFoundation
	"com.apple.SystemConfiguration.configd",   // network configuration for DNS and routing
	"com.apple.dnssd.service",                 // mDNSResponder, name resolution
}

// canonicalPrefixes maps the symlinked top-level directories of macOS to the
// real path. Seatbelt matches real paths, so a rule on /tmp would never match.
var canonicalPrefixes = [][2]string{
	{"/tmp", "/private/tmp"},
	{"/var", "/private/var"},
	{"/etc", "/private/etc"},
}

// canonical validates p and returns its cleaned, canonical absolute form.
func canonical(p string) (string, error) {
	if p == "" {
		return "", errors.New("macbox: empty path")
	}
	for _, r := range p {
		switch {
		case r == 0:
			return "", fmt.Errorf("macbox: path %q contains a NUL byte; refusing to render it", p)
		case r == '"' || r == '\\':
			return "", fmt.Errorf("macbox: path %q contains a quote or backslash; refusing to render it", p)
		case r < 0x20 || r == 0x7f:
			return "", fmt.Errorf("macbox: path %q contains a control character; refusing to render it", p)
		}
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("macbox: path %q is not absolute; pass an absolute path", p)
	}
	c := path.Clean(p)
	for _, m := range canonicalPrefixes {
		if c == m[0] || strings.HasPrefix(c, m[0]+"/") {
			c = m[1] + strings.TrimPrefix(c, m[0])
			break
		}
	}
	return c, nil
}

func subpath(p string) string { return `(subpath "` + p + `")` }

type builder struct {
	sb strings.Builder
}

func (b *builder) line(format string, a ...any) {
	fmt.Fprintf(&b.sb, format+"\n", a...)
}

// Profile renders the SBPL profile. Any invalid path returns an error and no
// profile.
func Profile(in ProfileInput) (string, error) {
	switch in.Role {
	case RoleReviewer, RoleQA, RoleCoder, RoleGate:
	default:
		return "", fmt.Errorf("macbox: unknown role %q (want reviewer, qa, coder or gate)", in.Role)
	}
	var err error
	canon := func(name, p string) string {
		if err != nil {
			return ""
		}
		var c string
		c, err = canonical(p)
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
		return c
	}
	ownerHome := canon("OwnerHome", in.OwnerHome)
	factoryHome := canon("FactoryHome", in.FactoryHome)
	agentDir := canon("AgentDir", in.AgentDir)
	cloneDir := canon("CloneDir", in.CloneDir)
	var ro []string
	for _, p := range in.ReadOnlyPaths {
		ro = append(ro, canon("ReadOnlyPaths", p))
	}
	if err != nil {
		return "", err
	}

	for name, p := range map[string]string{"FactoryHome": factoryHome, "AgentDir": agentDir, "CloneDir": cloneDir} {
		for _, d := range ownerSecretDirs {
			sd := ownerHome + "/" + d
			if p == sd || strings.HasPrefix(p, sd+"/") {
				return "", fmt.Errorf("macbox: %s %q lies inside the denied owner secret dir %q; move it", name, p, sd)
			}
		}
	}

	b := &builder{}
	b.line("(version 1)")
	b.line("(deny default)")
	b.line("")
	b.line(";; processes: run and spawn tools, signal and inspect only itself")
	b.line("(allow process-exec*)")
	b.line("(allow process-fork)")
	b.line("(allow signal (target self))")
	b.line("(allow process-info* (target self))")
	b.line("(allow sysctl-read)")
	b.line("")
	b.line(";; shared temp is closed first, so the allows below re-open what lies under it (the agent TMPDIR)")
	b.line("(deny file* %s)", subpath("/private/tmp"))
	b.line("")
	b.line(";; path lookup (stat, realpath, getcwd) on any path; metadata only, no contents")
	b.line("(allow file-read-metadata)")
	b.line("")
	b.line(";; read: system, tool installs, the factory checkout, the agent and clone dirs")
	for _, p := range systemReadPaths {
		b.line("(allow file-read* %s)", subpath(p))
	}
	for _, p := range systemReadLiterals {
		b.line(`(allow file-read* (literal "%s"))`, p)
	}
	b.line(`(allow file-read* (regex #"^/dev/tty[a-z0-9]*$"))`)
	for _, p := range ro {
		b.line("(allow file-read* %s)", subpath(p))
	}
	b.line("(allow file-read* %s)", subpath(factoryHome))
	b.line("(allow file-read* %s)", subpath(agentDir))
	b.line("(allow file-read* %s)", subpath(cloneDir))
	b.line("")
	b.line(";; write: agent dir, the factory db, the clone when writable")
	b.line(`(allow file-write-data (literal "/dev/null"))`)
	b.line(`(allow file-write-data (regex #"^/dev/tty[a-z0-9]*$"))`)
	b.line("(allow file-write* %s)", subpath(agentDir))
	b.line("(allow file-write* %s)", subpath(factoryHome+"/db"))
	if in.CloneWritable {
		b.line("(allow file-write* %s)", subpath(cloneDir))
	}
	b.line("")
	b.line(";; Mach services: fixed list, everything else is denied by default")
	for _, s := range machServices {
		b.line(`(allow mach-lookup (global-name "%s"))`, s)
	}
	if in.Network {
		b.line("")
		b.line(";; network")
		b.line("(allow network-outbound)")
		b.line("(allow system-socket)")
	}
	b.line("")
	b.line(";; owner secrets: explicit denies after every allow, so they always win")
	for _, d := range ownerSecretDirs {
		b.line("(deny file* %s)", subpath(ownerHome+"/"+d))
	}
	b.line("(deny file* %s)", subpath("/Library/Keychains")) // system keychains: /Library is readable minus this
	return b.sb.String(), nil
}
