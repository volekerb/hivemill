// Command macbox-spawn is the macOS side of `factory spawn` (ADR 0003 section 3.2, M6b). It runs as the owner.
//
//	macbox-spawn prepare   writes profile.sb and bin/claude for one agent and sets the clone ACLs
//	macbox-spawn preflight checks the clones, then runs macbox.Preflight (os, user, sudo, seatbelt, view, acl)
//
// Both print one JSON line {"ok":bool,"reason":"","why":""} and exit 0 (ok) or 3 (refused). A failure that is not
// a refusal prints to stderr and exits 2; the caller treats it as a refusal too (fail closed).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"

	"factory/go/internal/macbox"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, macbox.OSRunner{}, os.Getuid(), "")) }

type result struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	Why    string `json:"why"`
}

// run is main without the process: goos "" means the real runtime.GOOS (tests set it).
func run(args []string, stdout, stderr io.Writer, r macbox.Runner, ownerUID int, goos string) int {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "preflight") {
		fmt.Fprintln(stderr, "usage: macbox-spawn prepare|preflight --role R --owner-home H --factory-home F --agent-dir D --coder-clone C [--qa-clone Q] --claude P --oauth P --gh P [--launcher P]")
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var role, ownerHome, factoryHome, agentDir, coder, qa, claude, oauth, gh, launcher, sudo string
	fs.StringVar(&role, "role", "", "reviewer, qa or coder")
	fs.StringVar(&ownerHome, "owner-home", "", "the owner's home")
	fs.StringVar(&factoryHome, "factory-home", "", "the factory home")
	fs.StringVar(&agentDir, "agent-dir", "", "the per-agent dir")
	fs.StringVar(&coder, "coder-clone", "", "the task's coder clone")
	fs.StringVar(&qa, "qa-clone", "", "qa's own clone (qa only)")
	fs.StringVar(&claude, "claude", "", "the real claude binary")
	fs.StringVar(&oauth, "oauth", "", "OAuth token file")
	fs.StringVar(&gh, "gh", "", "gh token file")
	fs.StringVar(&launcher, "launcher", macbox.DefaultLauncher, "the root-owned launcher")
	fs.StringVar(&sudo, "sudo", macbox.DefaultSudo, "sudo path")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	say := func(rf *macbox.Refusal) int {
		res := result{OK: rf == nil}
		if rf != nil {
			res.Reason, res.Why = rf.Reason, rf.Why
		}
		b, _ := json.Marshal(res)
		fmt.Fprintln(stdout, string(b))
		if rf != nil {
			return 3
		}
		return 0
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if role != "qa" {
		qa = ""
	} else if qa == "" {
		return say(&macbox.Refusal{Reason: macbox.ReasonClone, Why: "qa needs its own clone (--qa-clone)"})
	}
	acct, err := macbox.AccountForRole(role)
	if err != nil {
		return say(&macbox.Refusal{Reason: macbox.ReasonUser, Why: err.Error()})
	}
	in, err := macbox.ForRole(macbox.Role(role), macbox.RolePaths{
		OwnerHome: ownerHome, FactoryHome: factoryHome, AgentDir: agentDir, CoderClone: coder, QAClone: qa,
	})
	if err != nil {
		return say(&macbox.Refusal{Reason: macbox.ReasonUser, Why: err.Error()})
	}
	profile := path.Join(agentDir, "profile.sb")
	if args[0] == "preflight" {
		if g := goos; g != "darwin" && (g != "" || runtime.GOOS != "darwin") {
			return say(&macbox.Refusal{Reason: macbox.ReasonOS, Why: "macbox is darwin-only"})
		}
		if rf := macbox.CheckClones(r, macbox.CloneSpec{CoderClone: coder, QAClone: qa}); rf != nil {
			return say(rf)
		}
		err := macbox.Preflight(macbox.Spec{
			GOOS: goos, OwnerUID: ownerUID, OwnerHome: ownerHome, FactoryHome: factoryHome, Role: role,
			Launcher: launcher, ProfilePath: profile, CloneDir: coder, AgentDir: agentDir,
			OAuthPath: oauth, GhPath: gh, ProfileIn: in,
		}, r)
		if err == nil {
			return say(nil)
		}
		if rf, ok := macbox.AsRefusal(err); ok {
			return say(rf)
		}
		return say(&macbox.Refusal{Reason: macbox.ReasonSeatbelt, Why: err.Error()})
	}
	text, err := macbox.Profile(in)
	if err != nil {
		return fail(err)
	}
	if err := macbox.PrepareAgentDirs(r, agentDir, acct); err != nil {
		return fail(err)
	}
	if err := r.WriteFile(profile, []byte(text), 0o644); err != nil {
		return fail(err)
	}
	if err := macbox.WriteShim(path.Join(agentDir, "bin", "claude"), macbox.ShimOpts{
		Role: role, LauncherPath: launcher, ProfilePath: profile, RealClaude: claude, AgentDir: agentDir,
		OAuthPath: oauth, GhPath: gh, SudoPath: sudo,
	}); err != nil {
		return fail(err)
	}
	switch role {
	case "coder":
		err = macbox.ApplyCloneACLs(r, coder, macbox.AccountRW, macbox.AccountRO)
	case "qa":
		err = macbox.ApplyCloneACLs(r, qa, macbox.AccountRO, "")
	}
	if err != nil {
		return fail(err)
	}
	return say(nil)
}
