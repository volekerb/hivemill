import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SYSTEMD = Path(__file__).resolve().parent.parent / "systemd"
HIVEMILL_UNITS = ["hivemill.service", "hivemill-digest.service", "hivemill-digest.timer",
                  "hivemill-health.service", "hivemill-health.timer"]
HIVEMILL_ENABLE = ["hivemill.service", "hivemill-digest.timer", "hivemill-health.timer"]


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-test-install-"))
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", str(self.tmp)]))
        stub = self.tmp / "bin"
        stub.mkdir()
        for tool in ("systemctl", "loginctl"):
            (stub / tool).write_text(f'#!/bin/sh\necho "{tool} $*" >> "{self.tmp}/calls"\n')
            (stub / tool).chmod(0o755)
        self.env = {"PATH": f"{stub}:/usr/bin:/bin", "XDG_CONFIG_HOME": str(self.tmp / "cfg"),
                    "HOME": str(self.tmp), "USER": "zz"}

    def install(self, *args):
        return subprocess.run(["bash", str(SYSTEMD / "install.sh"), *args], env=self.env,
                              capture_output=True, text=True)

    def test_dry_run_changes_nothing(self):
        p = self.install("--dry-run")
        self.assertEqual(p.returncode, 0, p.stderr)
        for u in HIVEMILL_ENABLE:
            self.assertIn(f"[dry-run] systemctl --user enable {u}", p.stdout)
        self.assertFalse((self.tmp / "cfg").exists())
        calls = self.tmp / "calls"  # only the read-only linger query may run
        self.assertNotIn("systemctl", calls.read_text() if calls.exists() else "")

    def test_unknown_flag_exits_2(self):
        for flag in ("--bogus", "--with-serve"):
            p = self.install(flag)
            self.assertEqual(p.returncode, 2, flag)
            self.assertIn("usage:", p.stderr)
            self.assertFalse((self.tmp / "cfg").exists())

    def test_unit_files_exist(self):
        for u in HIVEMILL_UNITS:
            self.assertTrue((SYSTEMD / u).is_file(), u)

    def test_install_idempotent_and_safe(self):
        for _ in range(2):
            self.assertEqual(self.install().returncode, 0)
        dest = self.tmp / "cfg" / "systemd" / "user"
        self.assertEqual(sorted(f.name for f in dest.iterdir()), sorted(HIVEMILL_UNITS))  # no factory-* or herdr unit
        for u in HIVEMILL_UNITS:
            self.assertEqual((dest / u).resolve(), (SYSTEMD / u).resolve())
        calls = (self.tmp / "calls").read_text()
        enabled = [ln.split()[-1] for ln in calls.splitlines() if ln.startswith("systemctl --user enable ")]
        self.assertEqual(enabled, HIVEMILL_ENABLE * 2)
        self.assertIn("--user daemon-reload", calls)
        self.assertNotIn("--now", calls)
        self.assertNotIn("start", calls)  # nothing is started or restarted

    def test_differing_unit_is_backed_up(self):
        dest = self.tmp / "cfg" / "systemd" / "user"
        dest.mkdir(parents=True)
        (dest / "hivemill.service").write_text("[Service]\nExecStart=/bin/true\n")
        self.assertEqual(self.install().returncode, 0)
        self.assertEqual((dest / "hivemill.service.bak").read_text(), "[Service]\nExecStart=/bin/true\n")
        self.assertTrue((dest / "hivemill.service").is_symlink())


class HerdrResumeTests(InstallTests):
    FLAG = "--disable-herdr-agent-resume"

    def cfg(self):
        return self.tmp / "cfg" / "herdr" / "config.toml"  # under $XDG_CONFIG_HOME

    def test_install_disable_herdr_agent_resume_idempotent_and_dry_run(self):
        cfg = self.cfg()
        p = self.install("--dry-run", self.FLAG)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("[dry-run] set [session] resume_agents_on_restore = false", p.stdout)
        self.assertFalse(cfg.exists())
        # existing config: other sections stay, the commented default is kept, a backup is made
        cfg.parent.mkdir(parents=True)
        original = '[ui]\nsound = true\n\n[session]\n# resume_agents_on_restore = true\nstartup_per_agent_delay_ms = 100\n'
        cfg.write_text(original)
        p = self.install("--dry-run", self.FLAG)
        self.assertEqual(cfg.read_text(), original)
        self.assertEqual(list(cfg.parent.glob("config.toml.bak-*")), [])
        self.assertEqual(self.install(self.FLAG).returncode, 0)
        text = cfg.read_text()
        self.assertEqual(text.count("resume_agents_on_restore = false"), 1)
        self.assertIn("[ui]\nsound = true", text)
        self.assertIn("startup_per_agent_delay_ms = 100", text)
        backups = list(cfg.parent.glob("config.toml.bak-*"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(), original)
        self.assertLess(text.index("[session]"), text.index("resume_agents_on_restore = false"))
        # idempotent: second run changes nothing and makes no new backup
        p = self.install(self.FLAG)
        self.assertIn("already has", p.stdout)
        self.assertEqual(cfg.read_text(), text)
        self.assertEqual(len(list(cfg.parent.glob("config.toml.bak-*"))), 1)
        # an explicit `= true` is flipped, not duplicated
        cfg.write_text("[session]\nresume_agents_on_restore = true\n")
        self.assertEqual(self.install(self.FLAG).returncode, 0)
        self.assertEqual(cfg.read_text(), "[session]\nresume_agents_on_restore = false\n")
        # no [session] section: appended
        cfg.write_text("[ui]\nsound = true\n")
        self.assertEqual(self.install(self.FLAG).returncode, 0)
        self.assertEqual(cfg.read_text().count("[session]"), 1)
        self.assertIn("resume_agents_on_restore = false", cfg.read_text())

    def test_herdr_config_honours_xdg_config_home(self):
        self.assertEqual(self.install(self.FLAG).returncode, 0)
        self.assertEqual(self.cfg().read_text(), "[session]\nresume_agents_on_restore = false\n")
        self.assertFalse((self.tmp / ".config" / "herdr").exists())  # not the $HOME default
        del self.env["XDG_CONFIG_HOME"]
        self.assertEqual(self.install(self.FLAG).returncode, 0)
        self.assertTrue((self.tmp / ".config" / "herdr" / "config.toml").exists())  # default when unset

    def test_default_install_leaves_herdr_config_alone(self):
        self.assertEqual(self.install().returncode, 0)
        self.assertFalse(self.cfg().exists())


class UnitTests(unittest.TestCase):
    def test_serve_unit_file_limits(self):
        text = (SYSTEMD / "factory-serve.service").read_text()
        for line in ("ExecStart=%h/factory/bin/factory serve", "Restart=always", "RestartSec=10", "Nice=10", "MemoryMax=200M", "MemoryHigh=150M",
                     "CPUQuota=50%", "NoNewPrivileges=yes", "PrivateTmp=yes", "ProtectSystem=strict", "ProtectHome=read-only",
                     "ReadWritePaths=%h/factory", "ReadOnlyPaths=%h/.claude", "WantedBy=default.target",
                     "ReadOnlyPaths=-%h/factory/bin -%h/factory/roles -%h/factory/systemd -%h/factory/web "
                     "-%h/factory/worktrees -%h/factory/.git -%h/factory/tests"):
            self.assertIn(line, text.splitlines())

    def test_units(self):
        up = (SYSTEMD / "factory-up.service").read_text()
        self.assertIn("ExecStart=%h/.local/bin/factory up", up)
        self.assertIn("WantedBy=default.target", up)
        self.assertIn("herdr workspace list", up)
        self.assertNotIn("network-online", up)
        for f in SYSTEMD.glob("*.service"):
            self.assertNotIn("server stop", f.read_text())


if __name__ == "__main__":
    unittest.main()
