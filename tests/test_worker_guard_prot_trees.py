"""T-0925: protected trees for workers (G-016 W2-T6h): glob targets, writer flags, `cd` then write, the Go toolchain and
Go config trees, four trees inside the live checkout, and the Edit tools. Every case goes through the real entry point
(guard_impl.hook_run: the hook as a subprocess, and with GUARD_IMPL=diff the Go binary too). The live checkout is a temp
dir named by WORKER_GUARD_LIVE_HOME, so a worktree under it is allowed (the hook's own repo root is never a live home)."""
import json
import os
import pwd
import shutil
import tempfile
import unittest

from guard_impl import hook_run

PW = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)  # the hook only reads the text of a command: nothing is written
_tmp = []


def setUpModule():
    root = os.path.realpath(tempfile.mkdtemp(prefix="prot-trees-"))
    _tmp.append(root)
    for d in ("other", "live/bin", "live/go", "live/roles/hooks", "live/roles/settings", "live/docs",
              "live/worktrees/G-000-T-0000/bin", "live/worktrees/G-000-T-0000/go", "live/worktrees/G-000-T-0000/roles/hooks"):
        os.makedirs(os.path.join(root, d))


def tearDownModule():
    for p in _tmp:
        shutil.rmtree(p, ignore_errors=True)


def paths():
    root = _tmp[0]
    return {"other": os.path.join(root, "other"), "live": os.path.join(root, "live"),
            "wt": os.path.join(root, "live", "worktrees", "G-000-T-0000")}


def run(cmd=None, cwd="other", tool=None, role="coder"):
    """Exit code of the hook for a Bash command (or a (tool name, file_path) call) from cwd other|live|wt."""
    p = paths()
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": p[cwd]}
    if tool:
        payload = {"tool_name": tool[0], "tool_input": {"file_path": tool[1]}, "cwd": p[cwd]}
    env = {**os.environ, "WORKER_GUARD_LIVE_HOME": p["live"]}
    return hook_run(json.dumps(payload), ["--role", role], env=env, cwd=p["other"])


class ProtTreesCase(unittest.TestCase):
    def blocked(self, cmds, cwds=("other", "live", "wt")):
        for cmd in cmds:
            for cwd in cwds:
                r = run(cmd.replace("@L@", paths()["live"]), cwd)
                self.assertEqual(r.returncode, 2, f"{cmd!r} from {cwd}: expected a block, got {r.returncode}")

    def allowed(self, cmds, cwds=("other", "live", "wt")):
        for cmd in cmds:
            for cwd in cwds:
                r = run(cmd.replace("@L@", paths()["live"]), cwd)
                self.assertEqual(r.returncode, 0, f"{cmd!r} from {cwd}: expected allow, got {r.returncode}: {r.stderr}")


class GlobTargets(ProtTreesCase):
    """Gap a: a glob that can match a protected path."""

    def test_glob_targets_blocked_for_every_family(self):
        self.blocked([
            "echo x > ~/.local/lib/fact*/worker-guard.stamp",   # ~/.local/lib/factory
            "echo x > @L@/run/settin?s/coder.json",              # <live>/run/settings
            "echo x > ~/.config/fact*/x",                        # ~/.config/factory
            "echo x > @L@/run/guard_imp?",                       # <live>/run/guard_impl
            "echo x > ~/.local/g?/go.env",                       # ~/.local/go
            "echo x > ~/.config/g[o]/env",                       # ~/.config/go
            "echo x > @L@/rol*/hooks/worker-guard.py",           # <live>/roles/hooks
            "cp /tmp/x ~/.local/lib/fact*/worker-guard",
            "tee ~/.config/f[a]ctory/live_home"])

    def test_look_alike_globs_and_reads_allowed(self):
        self.allowed(["ls ~/.config/fact*", "echo x > ~/.local/gox/y", "echo x > ~/.config/gopher/y",
                      "cat ~/.local/lib/fact*/worker-guard.stamp"])


class WriterFlags(ProtTreesCase):
    """Gaps b, e and f: writers the hook did not treat as writers."""

    def test_writer_flags_blocked(self):
        self.blocked([
            "curl -o ~/.local/lib/factory/worker-guard URL", "curl -sSLo ~/.local/go/go.env URL",
            "curl --output ~/.config/factory/x URL", "curl -o @L@/bin/x URL",
            "wget -O ~/.local/lib/factory/worker-guard URL", "wget --output-document=@L@/go/x URL",
            "wget -P ~/.local/go URL",
            "go build -o ~/.local/lib/factory/worker-guard ./cmd/x", "go build -o @L@/bin/x ./cmd/x",
            "gcc -o ~/.local/lib/factory/worker-guard a.c", "cc -o @L@/bin/x a.c", "clang -o ~/.local/go/x a.c",
            "tar -C ~/.local/lib/factory -xf a.tar", "tar xf a.tar --directory ~/.config/factory",
            "tar -C @L@/go -xf a.tar", "tar -cf ~/.local/go/x.tar foo",
            "unzip -d ~/.config/factory a.zip", "unzip -d ~/.local/go a.zip",
            "rsync -a /tmp/x/ ~/.local/lib/factory/", "install -m 755 /tmp/x @L@/bin/factory_cli.py",
            "go env -w GOFLAGS=-mod=mod", "go env -u GOFLAGS",
            # bundled short flags and neighbours (self-review)
            "tar -xCf @L@/go a.tar", "tar xCf @L@/go a.tar", "tar xfC a.tar @L@/go", "tar -xzfC a.tgz @L@/go",
            "unzip -qd @L@/go a.zip", "unzip -qod @L@/go a.zip", "unzip a.zip -qd @L@/go",
            "env -C @L@/bin tar xf a", "7z -y x a.7z -o@L@/bin"])

    def test_writer_flags_into_scratch_allowed(self):
        self.allowed(["curl -o /tmp/x URL", "wget -qO- URL", "go build -o /tmp/x ./cmd/x", "tar -C /tmp/x -xf a.tar",
                      "tar --one-top-level=/tmp/x -xf a.tar", "gcc -o /tmp/x a.c", "go env", "go env GOFLAGS",
                      "go env GOROOT GOPATH", "go version", "tar -tf a.tar", "unzip -l a.zip",
                      "tar -czf /tmp/x.tgz -C ~/.local/go .", "tar -xCf /tmp/x a.tar", "unzip -qd /tmp/x a.zip",
                      "env -C /tmp/x ls"], cwds=("other",))
        self.allowed(["go build -o bin/x ./cmd/x", "curl -O URL", "wget URL", "tar -xzf a.tgz", "unzip -qo a.zip"], cwds=("wt",))


class CdThenWrite(ProtTreesCase):
    """Gap c: a `cd` into a tree, then a writer with no target of its own."""

    def test_extractor_after_cd_blocked(self):
        self.blocked(["cd ~/.local/lib/factory && tar -xf a.tar", "cd ~/.config/factory && unzip a.zip",
                      "cd ~/.local/go && echo x > go.env", "cd @L@/bin && echo x > factory_cli.py",
                      "cd @L@/go && tar -xf a.tar", "cd @L@/roles/hooks && cp /tmp/x worker-guard.py",
                      "cd ~/.local/go && curl -O URL", "cd ~/.local/lib/factory && git clone URL",
                      "tar --one-top-level=$HOME/.local/lib/factory -xf a.tar"])

    def test_unresolved_cd_then_extractor_blocked(self):
        self.blocked(["cd $X && tar -xf a.tar", "cd $X && unzip a.zip"])

    def test_extractor_in_scratch_or_worktree_allowed(self):
        self.allowed(["cd /tmp/x && tar -xf a.tar", "cd /tmp/x && unzip a.zip"], cwds=("other",))
        r = run("tar -xf a.tar", "wt")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_unresolved_cd_then_plain_write_stays_allowed(self):
        # only an extractor has no target to look at: `cd $X && echo x > f` keeps its decision on main
        self.allowed(["cd $X && echo x > f"], cwds=("other",))


class LiveCheckoutTrees(ProtTreesCase):
    """Gap f: <live>/go, <live>/bin, <live>/roles/hooks, <live>/roles/settings, from every cwd."""

    def test_absolute_writes_blocked_from_every_cwd(self):
        self.blocked([
            "echo x > @L@/bin/factory_cli.py", "echo x >> @L@/bin/factory", "cp /tmp/x @L@/bin/factory_cli.py",
            "mv /tmp/x @L@/go/x.go", "ln -sf /tmp/x @L@/bin/factory_cli.py", "rm @L@/bin/factory_cli.py",
            "rm -r @L@/go", "tee @L@/roles/hooks/worker-guard.py", "echo x > @L@/roles/settings/coder.json",
            "echo x > @L@/rol*/hooks/worker-guard.py", "sed -i s/a/b/ @L@/bin/factory_cli.py",
            "cd @L@/bin && echo x > factory_cli.py"])

    def test_relative_write_follows_the_cwd(self):
        # the cwd table: the same relative command is refused in the live checkout and allowed in a worktree or elsewhere
        for cmd in ("echo x > bin/factory_cli.py", "echo x > roles/hooks/worker-guard.py", "cp /tmp/x go/x.go",
                    "rm bin/factory_cli.py", "go build -o ./bin/x ./cmd/x"):
            self.assertEqual(run(cmd, "live").returncode, 2, f"{cmd!r} from live")
            self.assertEqual(run(cmd, "wt").returncode, 0, f"{cmd!r} from a worktree")
            self.assertEqual(run(cmd, "other").returncode, 0, f"{cmd!r} from elsewhere")

    def test_reads_runs_look_alikes_and_other_paths_allowed(self):
        self.allowed(["cat @L@/bin/factory_cli.py", "ls @L@/go", "grep -rn foo @L@/roles/hooks",
                      "cp @L@/roles/settings/coder.json /tmp/x", "@L@/bin/factory whoami",
                      "echo x > @L@/binx/y", "echo x > @L@/gox/y", "echo x > @L@/README.md", "echo x > @L@/docs/a.md",
                      "echo x > @L@/worktrees/G-000-T-0000/bin/x", "cp /tmp/x @L@/worktrees/G-000-T-0000/go/x.go"],
                     cwds=("other", "wt"))


class HomeTrees(ProtTreesCase):
    """Gap e: ~/.local/go and ~/.config/go (the Go toolchain and `go env -w`)."""

    def test_blocked(self):
        self.blocked(["echo x > ~/.local/go/go.env", "echo x >> $HOME/.local/go/go.env", "cp /tmp/x ~/.local/go/go.env",
                      "rm -r ~/.local/go", "curl -o ~/.local/go/bin/go URL", "tar -C ~/.local/go -xf a.tar",
                      "echo x > ~/.config/go/env", "mv /tmp/x ~/.local/go/go.env", "tee ~/.local/go/go.env",
                      "go build -o ~/.local/go/bin/go ./cmd/x", "go -C ~/.local/go build ./..."])

    def test_allowed(self):
        self.allowed(["~/.local/go/bin/go version", "cat ~/.local/go/go.env", "ls ~/.config/go", "go env GOFLAGS",
                      "go build ./...", "cp ~/.local/go/go.env /tmp/x", "echo x > ~/.local/gox/y"], cwds=("other",))


class GitCloneTarget(ProtTreesCase):
    def test_clone_into_a_tree_blocked(self):
        self.blocked(["git clone URL ~/.local/lib/factory/x", "git clone URL ~/.config/factory/x",
                      "git clone URL @L@/bin/x", "git clone --depth 1 URL ~/.local/go/x",
                      "git clone --separate-git-dir ~/.local/go/g URL /tmp/z", "git -C ~/.local/go clone URL"],
                     cwds=("other", "wt"))

    def test_clone_elsewhere_allowed(self):
        self.allowed(["git clone URL /tmp/x", "git clone URL"], cwds=("other", "wt"))


class EditTools(unittest.TestCase):
    """Gap g: Write/Edit/MultiEdit/NotebookEdit were checked against the persist paths only."""

    def test_edit_tools_blocked_per_tree_family(self):
        live = paths()["live"]
        for path in ("~/.local/lib/factory/worker-guard", "~/.config/factory/live_home", "~/.local/go/go.env",
                     "~/.config/go/env", live + "/bin/factory_cli.py", live + "/go/cmd/x.go",
                     live + "/roles/hooks/worker-guard.py", live + "/roles/settings/coder.json.in",
                     live + "/run/settings/coder.json", live + "/run/guard_impl"):
            for tool in ("Write", "Edit", "MultiEdit", "NotebookEdit"):
                for cwd in ("other", "live", "wt"):
                    r = run(tool=(tool, path.replace("~", PW, 1)), cwd=cwd)
                    self.assertEqual(r.returncode, 2, f"{tool} {path} from {cwd}: got {r.returncode}")

    def test_relative_path_follows_the_cwd(self):
        self.assertEqual(run(tool=("Write", "bin/factory_cli.py"), cwd="live").returncode, 2)
        self.assertEqual(run(tool=("Write", "bin/factory_cli.py"), cwd="wt").returncode, 0)

    def test_worktree_and_look_alikes_allowed(self):
        live = paths()["live"]
        for path in (live + "/worktrees/G-000-T-0000/bin/x", live + "/worktrees/G-000-T-0000/go/x.go",
                     live + "/binx/y", live + "/README.md", PW + "/.local/gox/y", "/tmp/y"):
            for tool in ("Write", "Edit"):
                r = run(tool=(tool, path), cwd="wt")
                self.assertEqual(r.returncode, 0, f"{tool} {path}: {r.stderr}")


class AttemptTwoFindings(ProtTreesCase):
    """Review of 8bf50627: B1 trailing option, B3 archive reads, B5 abbreviated long options, B6 recursive glob, and the
    ~/.local/bin family (T-0957) for gaps a, b, c."""

    def test_b1_trailing_option_does_not_hide_the_destination(self):
        self.blocked(["cp /tmp/x ~/.local/go/go.env -f", "cp /tmp/x ~/.local/lib/factory/worker-guard -f",
                      "install /tmp/x ~/.local/go/x -m755", "install /tmp/x ~/.local/go/x -m 755",
                      "ln -s /tmp/x ~/.local/go/go.env -f", "rsync /tmp/x ~/.local/go/go.env -a",
                      "cp /tmp/x @L@/bin/factory_cli.py -f"])
        self.allowed(["cp /tmp/x /tmp/y -f", "cp ~/.local/go/go.env /tmp/x -f"])

    def test_b3_archive_list_and_diff_are_reads(self):
        self.allowed(["tar -tf ~/.local/go/pkg.tar", "tar --list -f ~/.local/go/pkg.tar", "tar -df ~/.local/go/pkg.tar"])
        self.blocked(["tar -cf ~/.local/go/pkg.tar x", "tar -rf ~/.local/go/pkg.tar x", "tar -uf ~/.local/go/pkg.tar x",
                      "tar --create --file=~/.local/go/pkg.tar x"])

    def test_b5_abbreviated_long_options(self):
        self.blocked(["tar --dir=~/.local/go -xf a.tar", "wget --output-doc=~/.local/go/x URL",
                      "tar --one-top=~/.local/go -xf a.tar"])
        self.allowed(["wget --output-doc=/tmp/x URL", "tar --dir=/tmp/x -xf a.tar"])

    def test_b6_double_star_matches_any_depth(self):
        self.blocked(["echo x > ~/**/factory_cli.py", "echo x > @L@/**/factory_cli.py"])
        self.allowed(["echo x > /opt/**/y"])

    def test_local_bin_family_for_gaps_a_b_c(self):
        self.blocked(["echo x > ~/.local/b*/git", "curl -o ~/.local/bin/git URL", "go build -o ~/.local/bin/x ./cmd/x",
                      "tar -C ~/.local/bin -xf a.tar", "cd ~/.local/bin && tar -xf a.tar", "unzip -d ~/.local/bin a.zip"])
        self.allowed(["ls ~/.local/bin", "echo x > ~/.local/binx/y", "tar -tf ~/.local/bin/a.tar"])


if __name__ == "__main__":
    unittest.main()
