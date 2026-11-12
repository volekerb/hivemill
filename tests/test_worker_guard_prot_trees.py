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
USER = pwd.getpwuid(os.getuid()).pw_name  # T-0983: the login is read at run time, never written into the repo
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


def run(cmd=None, cwd="other", tool=None, role="coder", fixture_live=True):
    """Exit code of the hook for a Bash command (or a (tool name, file_path) call) from cwd other|live|wt."""
    p = paths()
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": p.get(cwd, cwd)}
    if tool:
        payload = {"tool_name": tool[0], "tool_input": {"file_path": tool[1]}, "cwd": p.get(cwd, cwd)}
    env = {**os.environ, "WORKER_GUARD_LIVE_HOME": p["live"]}
    if not fixture_live:
        env.pop("WORKER_GUARD_LIVE_HOME")
    return hook_run(json.dumps(payload), ["--role", role], env=env, cwd=p["other"])


class ProtTreesCase(unittest.TestCase):
    def blocked(self, cmds, cwds=("other", "live", "wt")):
        for cmd in cmds:
            for cwd in cwds:
                r = run(cmd.replace("@L@", paths()["live"]).replace("@U@", USER), cwd)
                self.assertEqual(r.returncode, 2, f"{cmd!r} from {cwd}: expected a block, got {r.returncode}")

    def allowed(self, cmds, cwds=("other", "live", "wt")):
        for cmd in cmds:
            for cwd in cwds:
                r = run(cmd.replace("@L@", paths()["live"]).replace("@U@", USER), cwd)
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


class CliImplFlag(ProtTreesCase):
    """T-1336 (G-041 S1-c): <live>/run/cli_impl and ~/.local/lib/factory/factory-go are held like run/guard_impl."""

    def test_cli_impl_flag_and_binary_writes_blocked(self):
        self.blocked(["echo go > @L@/run/cli_impl", "rm @L@/run/cli_impl", "tee @L@/run/cli_impl", "echo x > @L@/run/cli_im?l",
                      "cp /tmp/x ~/.local/lib/factory/factory-go", "mv /tmp/x ~/.local/lib/factory/factory-go.stamp",
                      "cd @L@/run && echo go > cli_impl"])

    def test_cli_impl_reads_allowed(self):
        self.allowed(["cat @L@/run/cli_impl", "cat ~/.local/lib/factory/factory-go.stamp",
                      "cp ~/.local/lib/factory/factory-go /tmp/copy"])


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
                     live + "/run/settings/coder.json", live + "/run/guard_impl", live + "/run/cli_impl"):
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
        # AC16: a `**` target outside every protected tree stays allowed. Not /opt: on the CI runner ~/.local/go is a symlink
        # to the setup-go GOROOT under /opt/hostedtoolcache, and the guard resolves trees, so /opt/**/y can really match it.
        # The /tmp case runs with the real live homes: the temp fixture live home sits under /tmp, so /tmp/**/y really matches it.
        self.allowed(["echo x > /nonexistent-root/**/y"])
        for cwd in ("other", "live", "wt"):
            r = run("echo x > /tmp/**/y", cwd, fixture_live=False)
            self.assertEqual(r.returncode, 0, f"/tmp/**/y from {cwd}: {r.stderr}")

    def test_b6_double_star_matches_resolved_symlinked_tree(self):
        """The tree is matched by its resolved path too, so a ** pattern over the symlink target is blocked (the CI case)."""
        real = os.path.realpath(os.path.join(PW, ".local", "go"))
        if real == os.path.join(PW, ".local", "go"):
            self.skipTest("~/.local/go is not a symlink here")
        top = "/" + real.strip("/").split("/")[0]
        self.assertEqual(run("echo x > %s/**/y" % top).returncode, 2)

    def test_local_bin_family_for_gaps_a_b_c(self):
        self.blocked(["echo x > ~/.local/b*/git", "curl -o ~/.local/bin/git URL", "go build -o ~/.local/bin/x ./cmd/x",
                      "tar -C ~/.local/bin -xf a.tar", "cd ~/.local/bin && tar -xf a.tar", "unzip -d ~/.local/bin a.zip"])
        self.allowed(["ls ~/.local/bin", "echo x > ~/.local/binx/y", "tar -tf ~/.local/bin/a.tar"])


class TildeName(ProtTreesCase):
    """T-0983 form A: `~name` and `~+` resolve as the shell resolves them, then are checked like any other path."""

    def test_tilde_login_reaches_every_home_tree(self):
        self.blocked(["echo x > ~@U@/.local/bin/git", "cp /tmp/x ~@U@/.local/bin/git", "echo x > ~@U@/.local/lib/factory/x",
                      "echo x > ~@U@/.config/factory/x", "echo x > ~@U@/.local/go/go.env", "echo x > ~@U@/.config/go/env",
                      "curl -o ~@U@/.local/lib/factory/x URL", "cd ~@U@/.local/lib/factory && tar -xf a.tar",
                      "wget -O ~@U@/.local/bin/git URL", "tar -C ~@U@/.local/go -xf a.tar", "rm ~@U@/.local/bin/git",
                      "cd ~@U@/.local/go && echo x > go.env"], cwds=("other",))

    def test_tilde_login_reaches_the_real_live_checkout(self):
        # the real live home (no fixture override): <pw_dir>/factory/{bin,go,roles/hooks,roles/settings}
        for cmd in ("echo x > ~@U@/factory/bin/factory_cli.py", "echo x > ~@U@/factory/go/x.go",
                    "echo x > ~@U@/factory/roles/hooks/worker-guard.py", "echo x > ~@U@/factory/roles/settings/coder.json.in",
                    "find ~@U@/factory/bin -name x -delete"):
            r = run(cmd.replace("@U@", USER), "other", fixture_live=False)
            self.assertEqual(r.returncode, 2, f"{cmd!r}: got {r.returncode}")

    def test_tilde_plus_is_the_effective_cwd(self):
        self.blocked(["cd ~ && echo x > ~+/.local/bin/git", "cd ~/.local && cp /tmp/x ~+/bin/git",
                      "cd ~/.local/bin && echo x > ~+/git"], cwds=("other",))
        self.allowed(["cd /tmp && echo x > ~+/.local/bin/git"], cwds=("other",))  # that is /tmp/.local/bin/git

    def test_unresolvable_names_follow_the_unresolvable_rule(self):
        # a name with no passwd entry, and ~-, are not decidable: blocked when they spell a protected name, as for $X
        self.blocked(["echo x > ~nosuchuser123/.local/bin/git", "echo x > ~-/.local/bin/git",
                      "cp /tmp/x ~nosuchuser123/.config/factory/x", "cd ~nosuchuser123/.local/lib/factory && tar -xf a.tar",
                      "echo x > ~nosuchuser123/.local/go/go.env"], cwds=("other",))
        self.allowed(["echo x > ~nosuchuser123/y", "ls ~nosuchuser123/.local/bin"], cwds=("other",))

    def test_reads_look_alikes_and_scratch_allowed(self):
        self.allowed(["ls ~@U@/.local/bin", "cat ~@U@/.config/factory/x", "cp ~@U@/.local/go/go.env /tmp/x",
                      "echo x > ~@U@/.local/gox/y", "echo x > /tmp/ok", "~@U@/.local/bin/factory whoami"], cwds=("other",))

    def test_tilde_alone_and_tilde_slash_keep_their_decisions(self):
        self.blocked(["echo x > ~/.local/bin/git", "cp /tmp/x ~/"], cwds=("other",))
        self.allowed(["ls ~", "ls ~/", "cat ~/.local/bin/x"], cwds=("other",))

    def test_edit_tool_path_with_a_tilde_name(self):
        for path in ("~@U@/.local/bin/git", "~@U@/.config/factory/live_home", "~@U@/.local/go/go.env",
                     "~nosuchuser123/.local/bin/git"):
            r = run(tool=("Write", path.replace("@U@", USER)), cwd="other")
            self.assertEqual(r.returncode, 2, f"Write {path}: got {r.returncode}")
        self.assertEqual(run(tool=("Write", "/tmp/ok"), cwd="other").returncode, 0)


class CopyIntoParent(ProtTreesCase):
    """T-0983 form B: the destination holds a protected tree, and the entry the source makes could be, or contain, one."""

    def test_source_named_like_the_next_component_blocked(self):
        self.blocked(["cp /tmp/bin ~/.local/", "cp -r /tmp/bin ~/.local", "cp -r /tmp/go ~/.local/", "mv /tmp/bin ~/.local/",
                      "rsync -a /tmp/bin ~/.local/", "cp -r /tmp/factory ~/.local/lib", "cp -r /tmp/factory ~/.local/lib/",
                      "ln -s /tmp/bin ~/.local/", "install -t ~/.local /tmp/bin", "cp -t ~/.local -r /tmp/go",
                      "cp -r /tmp/bin/ ~/.local/",
                      "cp -r /tmp/bin @L@/", "cp -r /tmp/go @L@/", "cp -r /tmp/roles @L@/", "cp -r /tmp/hooks @L@/roles/",
                      "cp -r /tmp/settings @L@/roles/", "mv /tmp/bin @L@/", "rsync -a /tmp/hooks @L@/roles/"],
                     cwds=("other",))

    def test_source_basename_not_known_blocked(self):
        self.blocked(["cp -r /tmp/x/. ~/.local/", "cp -rT /tmp/x ~/.local", "cp --no-target-directory /tmp/x ~/.local",
                      "rsync -a /tmp/x/ ~/.local/", "cp /tmp/* ~/.local/", "cp -r /tmp/$X ~/.local/",
                      "cp -r /tmp/$(ls) ~/.local/", "cp /tmp/?in ~/.local/", "cp --parents /tmp/x ~/.local/",
                      "rsync -aR /tmp/./bin/x ~/.local/", "cp -r . ~/.local/", "cp -r .. ~/.local/",
                      "rsync -a /tmp/x/ @L@/roles/", "cp -rT /tmp/x @L@"], cwds=("other",))

    def test_archive_extracted_into_a_parent_blocked(self):
        self.blocked(["tar -C ~/.local -xf a.tar", "cd ~/.local && tar -xf a.tar", "unzip a.zip -d ~/.local",
                      "unzip -d ~/.local a.zip", "cd ~/.local && unzip a.zip", "tar --directory=~/.local -xf a.tar",
                      "tar --one-top-level=~/.local -xf a.tar", "tar -C ~/.config -xf a.tar", "tar -C ~ -xf a.tar",
                      "tar -C @L@ -xf a.tar", "tar -C @L@/roles -xf a.tar", "unzip -d @L@/roles a.zip",
                      "cd @L@ && tar -xf a.tar", "7z x a.7z -o@L@/roles"], cwds=("other",))

    def test_cd_into_the_parent_then_copy(self):
        self.blocked(["cd ~/.local && cp -r /tmp/bin .", "cd ~/.local && cp -r /tmp/go ./",
                      "cd ~/.local/lib && cp -r /tmp/factory ."], cwds=("other",))
        self.allowed(["cd ~/.local && cp /tmp/x ."], cwds=("other",))

    def test_other_source_names_keep_main_decision(self):
        self.allowed(["cp /tmp/x ~/.local/", "cp /tmp/x ~/.local/share/", "cp -r /tmp/bin /tmp/y/", "cp /tmp/x ~/.local/lib/",
                      "cp -r /tmp/x ~/.local/", "mv /tmp/x ~/.local/", "rsync -a /tmp/x ~/.local/", "cp -r /tmp/x @L@/",
                      "cp /tmp/x @L@/roles/", "cp -r /tmp/bin /tmp/y/ -f", "cp -r /tmp/bin ~/.local/share/",
                      "tar -tf a.tar", "tar -C /tmp/x -xf a.tar", "unzip -l a.zip", "tar -C ~/.local/share -xf a.tar"],
                     cwds=("other",))

    def test_copy_destination_followed_by_an_option_value_or_with_a_brace_blocked(self):
        # reviewer B2 and nit 1 @275f8f14: the option value must not be taken as the destination; a brace is expanded
        self.blocked(["rsync -a /tmp/bin ~/.local/ --exclude x", "install /tmp/bin ~/.local/ -m 755",
                      "cp -r /tmp/bin ~/.local/ -S .bak", "cp -r /tmp/bin ~/.{local,x}/"], cwds=("other",))
        self.allowed(["rsync -a /tmp/bin /tmp/y/ --exclude x", "cp -r /tmp/x ~/.local/ -S .bak"], cwds=("other",))

    def test_worktree_copies_and_extractions_allowed(self):
        self.allowed(["cp -r /tmp/bin .", "tar -xf a.tar", "tar -C . -xf a.tar", "cp -r /tmp/bin ./bin", "cp -r /tmp/go go/",
                      "unzip a.zip", "rsync -a /tmp/x/ ./", "cp -r /tmp/hooks roles/", "cp /tmp/x bin/"], cwds=("wt",))

    def test_extracting_into_the_live_home_is_refused_for_every_worker_role(self):
        # was allowed by test_worker_guard_nopush_gitfile.test_other_roles_unchanged (edited in T-0983): the live home holds bin/ and go/
        for role in ("coder", "judge", "cleaner"):
            r = run("unzip a.zip -d @L@".replace("@L@", paths()["live"]), "other", role=role)
            self.assertEqual(r.returncode, 2, f"{role}: unzip into the live home: got {r.returncode}")
            r = run("tar -xf a.tar -C " + paths()["live"], "other", role=role)
            self.assertEqual(r.returncode, 2, f"{role}: tar into the live home: got {r.returncode}")

    def test_already_blocked_lines_stay_blocked(self):
        self.blocked(["mv /tmp/factory ~/.config/", "cp -r /tmp/bin ~/factory/", "cp -r /tmp/settings ~/factory/run/",
                      "find ~/.config/factory -name x -delete", "cp -r /tmp/.local ~/", "cp /tmp/x ~/"], cwds=("other",))


class FindDeletes(ProtTreesCase):
    """T-0983 form C: a find that deletes, runs a writer or writes a file writes to every start path."""

    def test_find_writers_on_a_protected_start_path_blocked(self):
        self.blocked(["find ~/.local/bin -name git -delete", "find ~/.local/lib/factory -delete",
                      "find ~/.local/go -name go.env -delete", "find ~/.config/factory -name x -delete",
                      "find ~/.config/go -delete", "find @L@/bin -name x -delete", "find @L@/go -delete",
                      "find @L@/roles/hooks -delete", "find @L@/roles/settings -delete", "find @L@/run/settings -delete",
                      "find ~/.local/bin -name git -exec rm {} +", "find ~/.local/lib/factory -name x -exec rm {} +",
                      "find ~/.local/bin -name x -execdir rm {} \\;", "find ~/.local/bin -name x -ok rm {} \\;",
                      "find @L@/bin -name x -okdir mv {} /tmp/y \\;", "find ~/.local/go -exec sh -c 'rm $0' {} \\;",
                      "find ~/.local/bin -exec chmod 777 {} +", "find -H ~/.local/bin -delete", "find -L ~/.local/go -delete",
                      "find ~/.local/bin -fprint ~/.local/bin/x", "find ~/.config/go -fls ~/.config/go/x",
                      "find /tmp/x -fprint ~/.local/bin/x", "find /tmp/x -fprintf ~/.local/go/x %p"], cwds=("other",))

    def test_find_writers_on_an_ancestor_start_path_blocked(self):
        self.blocked(["find ~/.local -name git -delete", "find ~/.local -exec rm {} +", "find @L@ -name x -delete",
                      "find @L@/roles -delete", "find ~/.config -name x -delete"], cwds=("other",))

    def test_find_exec_of_any_command_on_a_protected_start_path_blocked(self):
        # reviewer B1 @275f8f14: git, zip, make and a relative script are writers the old lists left out
        self.blocked(["find ~/.local/go -maxdepth 0 -exec git init {} +", "find ~/.local/bin -name x -exec zip -m /tmp/a.zip {} +",
                      "find ~/.local/lib/factory -exec ./x.sh {} +", "find ~ -name x -exec make {} +"], cwds=("other",))

    def test_find_exec_of_a_read_only_command_or_outside_the_trees_allowed(self):
        self.allowed(["find ~/.local/bin -name x -exec ls {} +", "find ~/.local/lib/factory -type f -exec grep y {} +",
                      "find /tmp/x -exec git init {} +"], cwds=("other",))
        self.allowed(["find . -name '*.py' -exec sed -i s/a/b/ {} +"], cwds=("wt",))

    def test_n1_find_expression_after_an_escaped_semicolon_is_judged(self):
        """N1: `\\;` ended the command, so -delete, -fprint or a second -exec after it were never judged."""
        self.blocked(["find ~/.local/bin -exec ls {} \\; -delete", "find ~/.local/bin -exec cat {} \\; -fprint /tmp/x",
                      "find ~/.local/bin -exec ls {} \\; -exec rm {} +",
                      "find ~/.local/go -name x -exec grep y {} \\; -exec git init {} +"], cwds=("other",))
        self.allowed(["find ~/.local/bin -exec ls {} \\; -name y", "find /tmp/x -exec ls {} \\; -delete",
                      "find ~/.local/bin -exec cat {} \\; -print"], cwds=("other",))

    def test_n2_exec_read_only_list_compares_the_whole_word(self):
        """N2: ./cat and /tmp/grep are not the listed readers; a bare name or its /usr/bin, /bin path is."""
        self.blocked(["find ~/.local/bin -name x -exec ./cat {} +", "find ~/.local/bin -name x -exec /tmp/grep {} +",
                      "find ~/.local/bin -exec ./ls {} \\;", "find ~/.local/bin -exec /usr/local/bin/cat {} \\;"],
                     cwds=("other",))
        self.allowed(["find ~/.local/bin -exec /usr/bin/cat {} \\;", "find ~/.local/bin -exec /bin/ls {} +",
                      "find ~/.local/bin -exec cat {} +"], cwds=("other",))

    def test_n3_unresolvable_cd_keeps_the_known_cwd(self):
        """N3: `cd ~nosuchuser` / `cd ~-` stay a path relative to the known cwd (main's decision), so a later relative
        write from a protected cwd is still judged."""
        self.blocked(["cd ~/.local/bin && cd ~nosuchuser123 && echo x > git", "cd ~/.local/go && cd ~- && cp /tmp/x y",
                      "cd @L@/bin && cd ~nosuchuser123 && tar -xf a.tar"], cwds=("other",))
        self.allowed(["cd /tmp && cd ~nosuchuser123 && echo x > y"], cwds=("other",))

    def test_find_without_a_start_path_uses_the_cwd(self):
        self.blocked(["find -delete", "find -name x -delete", "find -exec rm {} +"], cwds=("live",))
        self.blocked(["cd ~/.local/bin && find -delete", "cd ~/.local/go && find -name x -delete",
                      "cd ~/.local/bin && find . -name x -delete", "cd @L@/bin && find -delete",
                      "cd @L@/roles/hooks && find -delete"], cwds=("other",))
        self.allowed(["find -delete", "find . -name '*.pyc' -delete", "find bin -name x -delete", "find go -name x -delete",
                      "find -name '*.pyc' -exec rm {} +"], cwds=("wt",))

    def test_find_reads_and_other_trees_allowed(self):
        self.allowed(["find /tmp/x -delete", "find ~/.local/bin -name git", "find ~/.local/lib/factory -type f",
                      "find ~/.cache/x -delete", "find ~/.local/bin -name git -exec ls {} \\;",
                      "find ~/.local -name git", "find @L@/bin -name x -print", "find ~/.local/go -name x -exec grep y {} +",
                      "find /tmp/x -name y -exec rm {} +", "find /tmp/x -fprint /tmp/y"], cwds=("other",))

    def test_find_in_a_worktrees_own_trees_allowed(self):
        self.allowed(["find @L@/worktrees/G-000-T-0000/bin -name x -delete", "find @L@/worktrees/G-000-T-0000/go -delete",
                      "find @L@/worktrees/G-000-T-0000 -name '*.pyc' -delete"], cwds=("other",))


TERMINATORS = ["\\;", "';'", '";"', '\\;""', '""\\;', "$';'"]
FIND_TAILS = ["-delete", "-fprint /tmp/y", "-exec rm {} +"]
# M23269: the one narrow exception to "same cells on /tmp/x exit 0": main already refuses the `$';'` terminator as a
# dynamic command word on any path, so those cells are expected to exit 2 on main and on the head (M3 wins over M1).
MAIN_EQUAL_EXCEPTIONS = [f"find /tmp/x -exec ls {{}} $';' {tail}" for tail in FIND_TAILS]
TILDE_WRITERS = ["echo x > {w}", "echo x >> {w}", "touch {w}", "echo x | tee {w}", "cp /tmp/x {w}", "mv /tmp/x {w}",
                 "rm {w}", "curl -o {w} URL"]
TILDE_WORDS = ["~nosuchuser123", "~-"]


def matrix_trees():
    """(spelling for a command, absolute path for a payload cwd) of one tree per family: two home trees, the config tree
    and the live-checkout fixture."""
    live = paths()["live"]
    return [("~/.local/bin", PW + "/.local/bin"), ("~/.local/lib/factory", PW + "/.local/lib/factory"),
            ("~/.config/factory", PW + "/.config/factory"), (live + "/bin", live + "/bin")]


class FindMatrix(ProtTreesCase):
    """M1 (T-0983 attempt 3): shlex drops quote state, so a quoted or escaped word made only of punctuation was read as an
    operator and cut the find. Every terminator spelling, grouping and punctuation -name, each followed by a write."""

    def cells(self, tree):
        for t in TERMINATORS:
            for tail in FIND_TAILS:
                yield f"find {tree} -exec ls {{}} {t} {tail}"
        for g, c in (("\\(", "\\)"), ("'('", "')'")):
            yield f"find {tree} {g} -name x {c} -delete"
        for n in (";", "&", "|", ">"):
            yield f"find {tree} -name '{n}' -delete"

    def test_every_cell_on_a_protected_tree_blocked(self):
        for tree, _ in matrix_trees():
            self.blocked(list(self.cells(tree)), cwds=("other",))

    def test_same_cells_outside_the_trees_allowed(self):
        cells = [c for c in self.cells("/tmp/x") if c not in MAIN_EQUAL_EXCEPTIONS]
        self.allowed(cells, cwds=("other",))
        self.blocked(MAIN_EQUAL_EXCEPTIONS, cwds=("other",))  # M23269: main refuses them too, so they stay exit 2

    def test_readers_without_a_write_tail_allowed(self):
        # `$';'` as above; ~/.config/factory is refused for any find -exec by main's config rules
        for tree, _ in matrix_trees():
            if tree.endswith(".config/factory"):
                continue
            self.allowed([f"find {tree} -exec ls {{}} {t} -print" for t in TERMINATORS if t != "$';'"] +
                         [f"find {tree} -name ';' -print", f"find {tree} \\( -name x \\) -print"], cwds=("other",))

    def test_worktree_grouping_allowed(self):
        self.allowed(["find . \\( -name x \\) -delete", "find . '(' -name x ')' -delete",
                      "find . -name ';' -delete"], cwds=("wt",))


class TildeMatrix(ProtTreesCase):
    """M2 (T-0983 attempt 3): an unresolvable `~name` or `~-` is a path relative to the cwd, as on main, so a write that
    reaches a protected tree through the cwd (a `cd`, or the payload cwd) is judged."""

    def test_cd_into_a_tree_then_write_blocked(self):
        for tree, _ in matrix_trees():
            for w in TILDE_WORDS:
                self.blocked([f"cd {tree} && {c.format(w=w)}" for c in TILDE_WRITERS], cwds=("other",))

    def test_payload_cwd_in_a_tree_then_write_blocked(self):
        for tree, real in matrix_trees():
            for w in TILDE_WORDS:
                self.blocked([c.format(w=w) for c in TILDE_WRITERS], cwds=(real,))

    def test_same_writes_from_scratch_allowed(self):
        for w in TILDE_WORDS:
            cmds = [c.format(w=w) for c in TILDE_WRITERS]
            self.allowed(cmds, cwds=("/tmp",))
            self.allowed(["cd /tmp && " + c for c in cmds], cwds=("other",))

    def test_spelled_names_stay_as_before(self):
        self.allowed(["echo x > ~nosuchuser123/y"], cwds=("/tmp",))
        self.blocked(["echo x > ~nosuchuser123/.local/bin/git", "echo x > ~-/.config/factory/x"], cwds=("/tmp",))


class ClosedListWritersT0988(ProtTreesCase):
    """T-0988: the eight closed-list writer forms into protected trees (patch and git were already closed on main)."""

    def test_blocked_into_protected_trees(self):
        p = paths()
        libd = os.path.join(os.path.expanduser("~"), ".local", "lib", "factory")
        bind = os.path.join(os.path.expanduser("~"), ".local", "bin")
        hooksd = os.path.join(p["live"], "roles", "hooks")
        bind_x = os.path.join(bind, "x")
        self.blocked([
            f"gzip -d {bind_x}.gz", f"gzip --decompress {bind_x}.gz", f"cd {bind} && gzip -d x.gz",
            f"cd {libd} && cpio -id < a.cpio", f"cpio -id -D {libd} < a.cpio",
            f"GOBIN={bind} go install ./cmd/x", f"env GOBIN={hooksd} go install ./cmd/x",
            f"gcc -MF {bind_x}.d -c a.c", f"gcc -MD -MF{bind_x}.d -c a.c",
            f"cargo build --target-dir {bind}", f"cargo build --target-dir={bind}",
            f"patch -o {bind_x} a b", f"patch -d {hooksd} -p1 -i a.diff",
            f"git init {bind_x}", f"git worktree add {os.path.join(p['live'], 'bin', 'y')}",
        ])

    def test_allowed_in_scratch_and_worktree(self):
        p = paths()
        bind = os.path.join(os.path.expanduser("~"), ".local", "bin")
        hooksd = os.path.join(p["live"], "roles", "hooks")
        bind_x = os.path.join(bind, "x")
        self.allowed([
            "patch -o /tmp/x a b", "gzip -d /tmp/x.gz", "gzip -dc ~/.local/bin/x.gz > /tmp/y",
            f"gzip -d --to-stdout {bind_x}.gz", f"cd {bind} && gzip -d",
            f"cd {bind} && cpio -i -t < a.cpio", f"cd {bind} && cpio -i --to-stdout < a.cpio",
            f"cpio -id -D {bind} --to-stdout < a.cpio",
            f"cd {hooksd} && cpio -i --only-verify-crc < a.cpio",
            f"cpio -i --only-verify-crc -D {bind} < a.cpio",
            f"cd {hooksd} && cpio --extract --only-verify-crc < a.cpio",
            "cpio -id < a.cpio", "git init /tmp/x", "git worktree add /tmp/x",
            "GOBIN=/tmp/b go install ./cmd/x", "gcc -MF /tmp/x.d -c a.c", "cargo build --target-dir /tmp/t",
        ], cwds=("other",))
        self.allowed([
            "patch -d bin -p1 -i a.diff", "gcc -MF go/x.d -c a.c", "cargo build --target-dir bin/t",
            "GOBIN=$PWD/bin go install ./cmd/x",
        ], cwds=("wt",))


class GitInitWorktreeAdd(ProtTreesCase):
    """G1: `git init <dir>` and `git worktree add <dir>` create a repository (hooks included) inside a protected tree."""

    def test_blocked_for_every_family(self):
        self.blocked(["git init ~/.local/go/x", "git worktree add ~/.local/go/x", "git init ~/.config/factory/x",
                      "git init ~/.local/lib/factory/x", "git init ~/.config/go/x", "git init ~/.local/bin/x",
                      "git init @L@/bin/x", "git init @L@/go/x", "git init @L@/roles/hooks/x", "git init @L@/roles/settings/x",
                      "git init @L@/run/settings/x", "git worktree add -b nb ~/.local/go/x", "git worktree add ~/.local/go/x main",
                      "git worktree add @L@/roles/settings/x", "git -C ~/.local/go init", "cd ~/.local/go && git init",
                      "git init --separate-git-dir ~/.local/go/g /tmp/z", "git init --template=/tmp/t ~/.local/go/x",
                      "git init -b main ~/.local/go/x", "git worktree move /tmp/x ~/.local/go/x", "git init ~@U@/.local/go/x"],
                     cwds=("other",))

    def test_elsewhere_allowed(self):
        self.allowed(["git init /tmp/x", "git worktree add /tmp/x", "git -C /tmp/x init", "git worktree list",
                      "git init -b main /tmp/x", "git worktree add -b nb /tmp/x main", "git init ~/.local/gox/x"],
                     cwds=("other",))
        self.allowed(["git init", "git worktree add ../other-wt"], cwds=("wt",))


if __name__ == "__main__":
    unittest.main()
