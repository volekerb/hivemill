"""T-0600: worker-guard refuses reading the owner-terms file (cat/grep/python/redirect/Read/Grep), through the real hook
with a temp FACTORY_HOME/DB and a fixture terms file; the two scanners stay allowed."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
ROLES = ("coder", "qa", "reviewer", "judge", "pm")


class TermsReadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-termsread-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        self.secrets = os.path.join(self.tmp, "secrets")
        os.makedirs(self.secrets)
        self.terms = os.path.join(self.secrets, "terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT INTO config VALUES ('scrub_terms_file', ?)", (self.terms,))
        con.commit()
        con.close()

    def hook(self, payload, role="coder", env_terms=True):
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env["FACTORY_HOME"] = self.home
        env["HOME"] = self.tmp  # $HOME and a bare cd resolve to the parent of the fixture secrets dir
        if env_terms:
            env["SCRUB_TERMS_FILE"] = self.terms
        payload = {"cwd": self.tmp, **payload}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=60, env=env)

    def bash(self, cmd, **kw):
        return self.hook({"tool_name": "Bash", "tool_input": {"command": cmd}}, **kw)

    def tool(self, name, **inp):
        return self.hook({"tool_name": name, "tool_input": inp})

    def refused(self, p, what):
        self.assertEqual(p.returncode, 2, f"should refuse: {what}\n{p.stderr}")
        self.assertIn("owner-terms", p.stderr)
        self.assertNotIn("zebracorp", p.stderr)
        self.assertNotIn(self.terms, p.stderr)

    def test_bash_read_shapes_refused_for_every_worker_role(self):
        shapes = ['cat "$SCRUB_TERMS_FILE"', "cat ${SCRUB_TERMS_FILE}", "head -n1 $SCRUB_TERMS_FILE",
                  f"cat {self.terms}", f"grep -r zebra {self.secrets}/terms.txt", "grep . < $SCRUB_TERMS_FILE",
                  "python3 -c 'print(open(__import__(\"os\").environ[\"SCRUB_TERMS_FILE\"]).read())'",
                  "sed p $SCRUB_TERMS_FILE", "cp $SCRUB_TERMS_FILE /tmp/x", "base64 $SCRUB_TERMS_FILE",
                  "xxd $SCRUB_TERMS_FILE", "strings $SCRUB_TERMS_FILE",
                  "python3 ci/scrub_scan.py x; cat $SCRUB_TERMS_FILE",
                  "python3 ci/scrub_scan.py $(cat $SCRUB_TERMS_FILE)", "echo $SCRUB_TERMS_FILE"]
        for role in ROLES:
            for cmd in shapes:
                self.refused(self.bash(cmd, role=role), f"{role}: {cmd}")

    def test_bash_refused_by_exported_path_even_when_config_is_empty(self):
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("DELETE FROM config")
        con.commit()
        con.close()
        self.refused(self.bash(f"cat {self.terms}"), "env path only")

    def test_scanners_allowed(self):
        for cmd in ["python3 ci/scrub_scan.py --text hello", "SCRUB_TERMS_FILE=$SCRUB_TERMS_FILE python3 ci/scrub_scan.py x",
                    'python3 ci/scrub_scan.py --terms "$SCRUB_TERMS_FILE" file', "python3 ci/public_text_guard.py --check x",
                    "env SCRUB_TERMS_FILE=/x/y python3 ./ci/scrub_scan.py a", "git status && python3 ci/scrub_scan.py",
                    'python3 ci/scrub_scan.py --terms "$SCRUB_TERMS_FILE" f 2>&1', "python3 ci/scrub_scan.py x > out.txt",
                    "env -i python3 ci/scrub_scan.py x"]:
            p = self.bash(cmd)
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_unrelated_commands_allowed_and_non_worker_roles_unaffected(self):
        self.assertEqual(self.bash("cat README.md").returncode, 0)
        self.assertEqual(self.bash("cat $SCRUB_TERMS_FILE", role="lead").returncode, 0)

    def test_path_shapes_resolving_to_the_terms_file_refused(self):
        d = self.secrets
        base = os.path.basename(d)
        os.makedirs(os.path.join(self.tmp, "wt"))
        shapes = [f"cd {d} && cat terms.txt", f"cat {d}/term?.txt", f"cat {d}/*.txt",
                  f"cat {d}/../{base}/terms.txt", f"cat {self.tmp}//{base}/terms.txt", f"cat {d}/'terms.txt'",
                  f'cat "{d}"/terms.txt', f"cd {d}; head -1 ./terms.txt", f"cd {self.tmp}/wt && cd ../{base} && cat t*.txt",
                  f"cat < {d}/terms.txt", f"echo $(cat {d}/terms.txt)", f"grep . --file={d}/terms.txt",
                  f"python3 ci/scrub_scan.py x && cd {d} && cat terms.txt"]
        for role in ROLES:
            for cmd in shapes:
                self.refused(self.bash(cmd, role=role), f"{role}: {cmd}")

    def test_recursive_and_archive_reads_of_the_dir_or_an_ancestor_refused(self):
        d, par = self.secrets, self.tmp
        base = os.path.basename(d)
        shapes = [f"grep -r zebra {d}", f"grep -rn . {d}", f"grep -R . {d}", f"rg . {d}", f"cd {par} && grep -r . {base}",
                  f"find {d} -type f -exec cat {{}} +", f"find {d} | xargs cat", f"find {d} -print0 | xargs -0 cat",
                  f"tar cf - {d} | tar xOf -", f"zip -r /tmp/x.zip {d}", f"cp -r {d} /tmp/x", f"rsync -a {d}/ /tmp/x/",
                  f"grep -r . {par}", f"cd {d} && grep -r .", f"cd {d} && rg zebra", f"grep --recursive . {d}"]
        for role in ROLES:
            for cmd in shapes:
                self.refused(self.bash(cmd, role=role), f"{role}: {cmd}")

    def test_variable_home_cd_shell_payload_xargs_and_dirflag_shapes_refused(self):
        d = self.secrets
        shapes = ["grep -r zebra $HOME", 'grep -r zebra "${HOME}"', "grep -r zebra ~", "cd; grep -r zebra secrets",
                  "cd && grep -r zebra .", "cd $HOME && grep -r zebra .", f"D={d}; grep -r zebra $D",
                  f"D={d}; grep -r zebra ${{D}}", f"find $HOME -name 't*.txt' -exec cat {{}} +",
                  f"echo {d} | xargs grep -r zebra", f"grep -d recurse zebra {d}", f"grep --directories=recurse zebra {d}",
                  f"bash -c 'grep -r zebra {d}'", f'sh -c "cd {d} && grep -r zebra ."', f"bash -c 'cat {d}/terms.txt'",
                  "tar cf - $HOME | tar xOf -"]
        for role in ROLES:
            for cmd in shapes:
                self.refused(self.bash(cmd, role=role), f"{role}: {cmd}")

    def test_shell_payload_with_inner_cd_refused_from_a_cwd_that_is_not_an_ancestor(self):
        d, par = self.secrets, self.tmp
        work = tempfile.mkdtemp(prefix="work")
        self.addCleanup(shutil.rmtree, work, True)
        base = os.path.basename(d)
        shapes = [f'sh -c "cd {d} && grep -r zebra"', f"sh -c 'cd {d} && grep -r zebra'", f"bash -c 'cd {d}; grep -r zebra'",
                  f"bash -c 'cd {par} && grep -r zebra {base}'", f'eval "cd {d} && grep -r zebra"']
        for role in ROLES:
            for cmd in shapes:
                p = self.hook({"cwd": work, "tool_name": "Bash", "tool_input": {"command": cmd}}, role=role)
                self.refused(p, f"{role}: {cmd}")
        other = os.path.join(work, "src")
        os.makedirs(other)
        for cmd in [f"sh -c 'cd {other} && grep -r x .'", f'echo "a; b | c"', f"bash -c 'cd {other}; ls'"]:
            p = self.hook({"cwd": work, "tool_name": "Bash", "tool_input": {"command": cmd}})
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_variable_home_and_shell_payloads_elsewhere_allowed(self):
        other = os.path.join(self.tmp, "src")
        os.makedirs(other)
        for cmd in [f"D={other}; grep -r x $D", f"bash -c 'grep -r x {other}'", f"cd {other}; cd; ls",
                    "echo $HOME", f"grep -d skip x {other}/a", "ls | xargs wc -l"]:
            p = self.bash(cmd)
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_recursive_reads_elsewhere_allowed(self):
        other = os.path.join(self.tmp, "src")
        os.makedirs(other)
        for cmd in [f"grep -r x {other}", f"rg x {other}", f"find {other} -name x", f"cd {other} && grep -rn x .",
                    f"cp -r {other} {other}2", "grep x README.md", f"cd {other} && rg x"]:
            p = self.bash(cmd)
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_indirection_and_brace_expansion_next_to_the_terms_file_refused(self):
        shapes = ['V=SCRUB_TERMS_FILE; cat "${!V}"', 'v=SCRUB; V=${v}_TERMS_FILE; cat "${!V}"',
                  f"cat {self.secrets}/terms.{{txt,x}}", "cat $SCRUB_TERMS_FIL{E,}", f"cat {self.secrets}/{{terms,x}}.txt"]
        for cmd in shapes:
            self.refused(self.bash(cmd), cmd)
        for cmd in ["echo ${!x}", "echo {a,b}", "x=1; echo ${!x}"]:
            self.assertEqual(self.bash(cmd).returncode, 0, cmd)

    def test_symlink_and_hardlink_to_the_terms_file_refused(self):
        os.symlink(self.terms, os.path.join(self.tmp, "ln.txt"))
        os.link(self.terms, os.path.join(self.tmp, "hard.txt"))
        self.refused(self.bash("cat ln.txt"), "symlink")
        self.refused(self.bash("cat hard.txt"), "hardlink")

    def test_unrelated_paths_next_to_it_allowed(self):
        with open(os.path.join(self.secrets, "other.md"), "w") as f:
            f.write("x")
        for cmd in [f"cat {self.secrets}/other.md", f"cd {self.secrets} && ls", "cd /tmp && cat /etc/hostname",
                    "echo \"it's fine\""]:
            p = self.bash(cmd)
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_unstatable_terms_file_fails_closed(self):
        os.remove(self.terms)
        self.refused(self.bash(f"cd {self.secrets} && cat terms.txt"), "gone file, basename")
        self.refused(self.bash(f"cat {self.secrets}/t*.txt"), "gone file, glob")
        self.assertEqual(self.bash("cat README.md").returncode, 0)

    def test_pm_read_tool_refused(self):
        p = self.hook({"tool_name": "Read", "tool_input": {"file_path": self.terms}}, role="pm")
        self.refused(p, "pm Read")

    def test_read_and_grep_tools(self):
        self.refused(self.tool("Read", file_path=self.terms), "Read path")
        self.refused(self.tool("Read", file_path=os.path.join(self.secrets, "..", "secrets", "terms.txt")), "Read ..")
        self.refused(self.tool("Read", file_path="$SCRUB_TERMS_FILE"), "Read var")
        self.refused(self.tool("Grep", pattern=".", path=self.terms), "Grep file")
        self.refused(self.tool("Grep", pattern=".", path=self.secrets), "Grep dir holding it")
        link = os.path.join(self.tmp, "link.txt")
        os.symlink(self.terms, link)
        self.refused(self.tool("Read", file_path=link), "Read symlink")
        other = os.path.join(self.tmp, "other.txt")
        with open(other, "w") as f:
            f.write("x")
        self.assertEqual(self.tool("Read", file_path=other).returncode, 0)
        sub = os.path.join(self.tmp, "sub")
        os.makedirs(sub)
        self.assertEqual(self.tool("Grep", pattern="x", path=sub).returncode, 0)


if __name__ == "__main__":
    unittest.main()
