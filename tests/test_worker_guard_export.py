"""T-0883: workers cannot forge run/guard-export.json (the Go guard's own-goal / push-pin / sandbox / terms decisions).

Bash writes, moves, links, removals and chmods of the file or its atomic-rename temp sibling, and Edit-class tool calls
on it, exit 2; reads stay allowed. Runs through the real entry point; GUARD_IMPL=diff compares Python with Go."""
import json
import os
import pwd
import shutil
import tempfile
import unittest

from guard_impl import hook_run

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SETTINGS = os.path.join(ROOT, "roles", "settings")
PW = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)
EXP = f"{PW}/factory/run/guard-export.json"
TMP = EXP + ".tmp4242"
EDIT_TOOLS = ("Edit", "Write", "MultiEdit", "NotebookEdit")
WRITES = [
    f"echo '{{}}' > {EXP}",
    f"echo '{{}}' >> {EXP}",
    f"echo '{{}}' > {TMP}",
    f"cp /tmp/x {EXP}",
    f"cp /tmp/x {TMP}",
    f"mv /tmp/x {EXP}",
    f"mv {EXP} /tmp/x",
    f"ln -sf /tmp/x {EXP}",
    f"echo x | tee {EXP}",
    f"install -m 600 /tmp/x {EXP}",
    f"rm -f {EXP}",
    f"rm {TMP}",
    f"chmod 666 {EXP}",
    f"truncate -s 0 {EXP}",
    f"python3 -c \"open('{EXP}','w').write('{{}}')\"",
    "python3 -c \"open('/h/run/'+'guard-export'+'.json','w')\"",
    "perl -e 'open(F, \">\", \"" + EXP + "\")'",
    f"sh -c 'echo {{}} > {EXP}'",
    f"bash -c \"sh -c 'cp /tmp/x {EXP}'\"",
    "cd " + PW + "/factory/run && echo '{}' > guard-export.json",
    f"sed -i s/a/b/ {EXP}",
    f"echo x > {PW}/factory/run/guard-expor*",
    f"echo x > {PW}/factory/run/g?ard-export.json",
    f"echo x > {PW}/factory/run/guard-[e]xport.json",
    f"echo x > {PW}/factory/run/guard-export.json.tmp*",
    f"cd {PW}/factory/run; echo x > guard-*",
    f"cd {PW}/factory/run && : > gua*",
    f"cd {PW}/factory/run && tee < /tmp/x gu*",
    f"rm {PW}/factory/run/*",
    f"cp /tmp/guard-export.json {PW}/factory/run/",
    f"cp -t {PW}/factory/run /tmp/guard-export.json",
    f"install -t {PW}/factory/run /tmp/guard-export.json",
    f"rsync /tmp/guard-export.json {PW}/factory/run/",
    f"mv /tmp/guard-export.json.tmp4242 {PW}/factory/run/",
    f"cd {PW}/factory/run && cp /tmp/guard-export.json .",
    f"cd {PW}/factory/run && mv /tmp/guard-export.json .",
    f"cd {PW}/factory/run && mv /tmp/guard-export.json.tmp4242 .",
    f"ln -s {PW}/factory/run /tmp/zr && echo x > /tmp/zr/guard-export.json",
    f"ln -s {PW}/factory /tmp/zf && echo x > /tmp/zf/run/guard-export.json",
    f"f=guard-export.json; echo x > {PW}/factory/run/$f",
    f"cd {PW}/factory/run && f=guard-export.json && echo x > $f",
]
READS = [f"cat {EXP}", f"jq .config {EXP}", f"grep generated_at {EXP}", f"ls -l {PW}/factory/run", f"head -c 20 {EXP}",
         f"cp /tmp/x {PW}/factory/run/other.log",
         f"echo x > {PW}/factory/run/other-*.log", "echo x > /tmp/zz-*.txt"]


RUN = f"{PW}/factory/run"
# T-0883 frozen probe table (rule R): every form exits 2 in python, go and diff; ~ and absolute spellings of RUN
PROBES = [
    f"cp -rt {RUN} src", f"install -Dt {RUN} src", f"cp -t{RUN} src", f"cp --target={RUN} src",
    f"cp --target-directory={RUN} src", f"ln -s X {RUN}/", f"ln X {RUN}", f"ln -s -t DIR {RUN}", f"ln -st DIR {RUN}",
    f"ln -s {RUN} /tmp/zr && echo x > /tmp/zr/guard-export.json", "ln -s ~ /tmp/zh && echo x > /tmp/zh/factory/run/g",
    "ln -s $HOME /tmp/zh && echo x > /tmp/zh/factory/run/g", "ln -s / /tmp/zs && echo x > /tmp/zs${HOME}/factory/run/g",
    f"ln -s {PW} /tmp/zp && echo x > /tmp/zp/factory/run/guard-export.json",
    "cp x ~/factory/run/", "mv /tmp/x ~/factory/run/guard-export.json.tmp1", "cp /tmp/s/g* " + RUN + "/",
    f"mv /tmp/s/g* {RUN}/", f"cp -t {RUN} /tmp/s/g*", f"rsync -a dir/ {RUN}/", f"cp -rT dir {RUN}",
    f"cp -a z/run {PW}/factory/", f"cp -rs {RUN} /tmp/x", f"cd {RUN} && cp x .", f"cd {RUN} && mv x .",
    f"f=guard-export.json; echo x > {RUN}/$f", f"cp {EXP} /tmp/copy-of-export", f"cd {RUN} && cp {EXP} /tmp/c",
    f"rm -rf {PW}/factory", f"chmod 777 {PW}/factory/run", "touch ~/factory/run", f"tar -C {RUN} -xf x.tar",
    f"tar xf x.tar --directory={RUN}", f"dd if=/dev/zero of={EXP}", f"cp x {PW}/", "cp x /", f"cp x ~/",
]
PROBES_ALLOWED = [f"cat {EXP}", f"jq . {EXP}", f"grep a {EXP}", f"head {EXP}", f"cp x {RUN}/other.log",
                  "cp -r src /tmp/dst", "rm -rf /tmp/zz-build", "tar czf /tmp/o.tgz src"]


def run(cmd, role="coder"):
    return hook_run(json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}), ["--role", role])


def edit(tool, path, role="coder"):
    key = "notebook_path" if tool == "NotebookEdit" else "file_path"
    return hook_run(json.dumps({"tool_name": tool, "tool_input": {key: path}, "cwd": ROOT}), ["--role", role])


class ExportGuardTest(unittest.TestCase):
    def test_bash_writes_are_blocked(self):
        for cmd in WRITES:
            r = run(cmd)
            self.assertEqual(r.returncode, 2, f"{cmd}: {r.stderr}")

    def test_frozen_probe_table_is_blocked_with_the_export_message(self):
        for cmd in PROBES:
            r = run(cmd)
            self.assertEqual(r.returncode, 2, f"{cmd}: {r.stderr}")
            self.assertIn("guard-export.json", r.stderr, cmd)

    def test_frozen_probe_table_allows_reads_and_unrelated_writes(self):
        for cmd in PROBES_ALLOWED:
            r = run(cmd)
            self.assertEqual(r.returncode, 0, f"{cmd}: {r.stderr}")

    def test_block_message_names_the_file(self):
        r = run(f"echo x > {EXP}")
        self.assertIn("guard-export.json", r.stderr)

    def test_reads_are_allowed(self):
        for cmd in READS:
            r = run(cmd)
            self.assertEqual(r.returncode, 0, f"{cmd}: {r.stderr}")

    def test_symlink_form_is_blocked(self):
        d = tempfile.mkdtemp(prefix="zz-test-export-")
        self.addCleanup(shutil.rmtree, d)
        link = os.path.join(d, "alias.json")
        os.symlink(EXP, link)
        self.assertEqual(run(f"echo x > {link}").returncode, 2)
        self.assertEqual(edit("Write", link).returncode, 2)
        self.assertEqual(run(f"cat {link}").returncode, 0)

    def test_edit_tools_are_blocked_on_file_and_temp_sibling(self):
        for tool in EDIT_TOOLS:
            for p in (EXP, TMP, f"{PW}/factory/run/../run/guard-export.json"):
                r = edit(tool, p)
                self.assertEqual(r.returncode, 2, f"{tool} {p}: {r.stderr}")
                self.assertIn("guard-export.json", r.stderr)

    def test_edit_tools_elsewhere_are_allowed(self):
        for p in (f"{ROOT}/x.txt", f"{PW}/factory/run/other.json"):
            self.assertEqual(edit("Write", p).returncode, 0, p)

    def test_unrelated_bash_is_allowed(self):
        self.assertEqual(run("echo hi > /tmp/zz-export-note.txt").returncode, 0)


class SettingsDenyTest(unittest.TestCase):
    def test_every_role_template_denies_edit_tools_on_export_and_temp_sibling(self):
        names = sorted(n for n in os.listdir(SETTINGS) if n.endswith(".json.in"))
        self.assertTrue(names)
        for n in names:
            with open(os.path.join(SETTINGS, n)) as f:
                text = f.read().replace("@FACTORY_LIVE_HOME@", "/live")
            for k in ("@FACTORY_USER_CONFIG@", "@FACTORY_HOME_DIR@"):
                text = text.replace(k, "/x")
            deny = json.loads(text)["permissions"]["deny"]
            for tool in EDIT_TOOLS:
                # the trailing * covers guard-export.json and guard-export.json.tmp<pid>
                self.assertIn(f"{tool}(//live/run/guard-export.json*)", deny, f"{n} {tool}")


if __name__ == "__main__":
    unittest.main()
