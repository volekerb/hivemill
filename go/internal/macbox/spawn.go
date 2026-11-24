package macbox

import (
	"fmt"
	"path"
	"strings"
)

// Clone ACLs of ADR 0003 section 3.2 (M6b). The coder clone gives _hivemill_rw
// read-write and _hivemill_ro read-only; qa's own clone gives _hivemill_ro
// read-write and no _hivemill_rw entry. Entries inherit to new files.
const (
	chmodPath = "/bin/chmod"
	lsPath    = "/bin/ls"

	aclReadRights = "read,execute,readattr,readextattr,readsecurity,list,search,file_inherit,directory_inherit"
	aclRWRights   = "read,write,execute,append,delete,delete_child,readattr,writeattr,readextattr,writeextattr,readsecurity,list,search,add_file,add_subdirectory,file_inherit,directory_inherit"
)

// writeRights are the ACL rights that let an account change a clone.
var writeRights = map[string]bool{
	"write": true, "append": true, "add_file": true, "add_subdirectory": true, "delete": true,
	"delete_child": true, "writeattr": true, "writeextattr": true, "writesecurity": true, "chown": true,
}

// ACLEntry is one parsed line of `ls -lde` ACL output.
type ACLEntry struct {
	Account string
	Allow   bool
	Rights  []string
}

// CanWrite reports an allow entry with any write right.
func (e ACLEntry) CanWrite() bool {
	if !e.Allow {
		return false
	}
	for _, r := range e.Rights {
		if writeRights[r] {
			return true
		}
	}
	return false
}

// ParseACL parses the ACL lines of `ls -lde <dir>` output ("0: user:_x allow read,list"). Lines that are not ACL
// entries (the mode line) are skipped; a malformed entry line is an error so an unreadable ACL never passes.
func ParseACL(out string) ([]ACLEntry, error) {
	var es []ACLEntry
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasSuffix(f[0], ":") || strings.Trim(f[0], "0123456789:") != "" {
			continue
		}
		if len(f) != 4 || (f[2] != "allow" && f[2] != "deny") {
			return nil, fmt.Errorf("unparseable ACL entry %q", strings.TrimSpace(line))
		}
		who, ok := strings.CutPrefix(f[1], "user:")
		if !ok {
			who = f[1] // group: and other kinds keep their prefix and never match a worker account
		}
		es = append(es, ACLEntry{Account: who, Allow: f[2] == "allow", Rights: strings.Split(f[3], ",")})
	}
	return es, nil
}

// ReadACL returns the ACL entries of dir.
func ReadACL(r Runner, dir string) ([]ACLEntry, error) {
	exit, out, errs := r.Run(lsPath, "-lde", dir)
	if exit != 0 {
		return nil, fmt.Errorf("ls -lde %s exit %d: %s", dir, exit, strings.TrimSpace(errs))
	}
	return ParseACL(out)
}

// ApplyCloneACLs replaces the ACLs of clone with one inherited read-write entry for writer and, when reader is not
// empty, one inherited read-only entry for reader. Old entries are removed first (-R -N), so a stale write entry
// never survives a re-spawn.
func ApplyCloneACLs(r Runner, clone, writer, reader string) error {
	if !path.IsAbs(clone) {
		return fmt.Errorf("clone path %q is not absolute", clone)
	}
	steps := [][]string{{"-R", "-N", clone}, {"-R", "+a", "user:" + writer + " allow " + aclRWRights, clone}}
	if reader != "" {
		steps = append(steps, []string{"-R", "+a", "user:" + reader + " allow " + aclReadRights, clone})
	}
	for _, a := range steps {
		if exit, _, errs := r.Run(chmodPath, a...); exit != 0 {
			return fmt.Errorf("chmod %s exit %d: %s", strings.Join(a[:len(a)-1], " "), exit, strings.TrimSpace(errs))
		}
	}
	return nil
}

// CloneSpec is the clone layout a read-write role's spawn is checked against.
type CloneSpec struct {
	CoderClone string
	QAClone    string // empty when the role has no own clone (reviewer, coder)
}

// CheckClones refuses with reason clone when _hivemill_ro would get a write entry on a clone a read-write role
// works in (the coder clone), when the qa clone carries any _hivemill_rw entry, or when the qa clone is the coder
// clone or not a sibling of it. Anything that cannot be read refuses.
func CheckClones(r Runner, c CloneSpec) *Refusal {
	if !path.IsAbs(c.CoderClone) {
		return refuse(ReasonClone, "coder clone %q is not an absolute path", c.CoderClone)
	}
	coder, err := ReadACL(r, c.CoderClone)
	if err != nil {
		return refuse(ReasonClone, "read coder clone ACL: %v", err)
	}
	for _, e := range coder {
		if e.Account == AccountRO && e.CanWrite() {
			return refuse(ReasonClone, "%s has a write entry on the coder clone %s (%s); remove it with chmod -R -N and respawn", AccountRO, c.CoderClone, strings.Join(e.Rights, ","))
		}
	}
	if c.QAClone == "" {
		return nil
	}
	if !path.IsAbs(c.QAClone) || path.Clean(c.QAClone) == path.Clean(c.CoderClone) || path.Dir(c.QAClone) != path.Dir(c.CoderClone) {
		return refuse(ReasonClone, "qa clone %q must be its own sibling of the coder clone %q", c.QAClone, c.CoderClone)
	}
	qa, err := ReadACL(r, c.QAClone)
	if err != nil {
		return refuse(ReasonClone, "read qa clone ACL: %v", err)
	}
	for _, e := range qa {
		if e.Account == AccountRW {
			return refuse(ReasonClone, "%s has an entry on the qa clone %s; qa must not share a clone with the coder", AccountRW, c.QAClone)
		}
	}
	return nil
}

// PrepareClones applies the ACLs for a spawn: the coder clone to rw (+ro read-only), the qa clone, when
// there is one, to ro read-write with no rw entry.
func PrepareClones(r Runner, c CloneSpec) error {
	if err := ApplyCloneACLs(r, c.CoderClone, AccountRW, AccountRO); err != nil {
		return err
	}
	if c.QAClone != "" {
		return ApplyCloneACLs(r, c.QAClone, AccountRO, "")
	}
	return nil
}

// PrepareAgentDirs creates the per-agent dirs (DirsFor) and gives the worker account read-write on HOME and TMPDIR
// only. The agent dir itself, bin/claude, profile.sb and gitconfig stay owner-owned and not writable by workers.
func PrepareAgentDirs(r Runner, agentDir, account string) error {
	d := DirsFor(agentDir)
	for _, p := range []string{agentDir, path.Join(agentDir, "bin"), d.Home, d.TmpDir} {
		if exit, _, errs := r.Run("/bin/mkdir", "-p", p); exit != 0 {
			return fmt.Errorf("mkdir %s exit %d: %s", p, exit, strings.TrimSpace(errs))
		}
	}
	if err := r.WriteFile(d.GitConfig, []byte("# hivemill agent gitconfig\n"), 0o644); err != nil {
		return err
	}
	for _, p := range []string{d.Home, d.TmpDir} {
		if exit, _, errs := r.Run(chmodPath, "-R", "+a", "user:"+account+" allow "+aclRWRights, p); exit != 0 {
			return fmt.Errorf("chmod +a %s exit %d: %s", p, exit, strings.TrimSpace(errs))
		}
	}
	return nil
}
