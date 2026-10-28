import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SYSTEMD = Path(__file__).resolve().parent.parent / "systemd"


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
        self.assertIn("enable factory-up.service", p.stdout)
        self.assertFalse((self.tmp / "cfg").exists())
        calls = self.tmp / "calls"  # only the read-only linger query may run
        self.assertNotIn("systemctl", calls.read_text() if calls.exists() else "")

    def test_serve_unit_is_opt_in(self):
        p = self.install("--dry-run")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertNotIn("factory-serve", p.stdout + p.stderr)
        p = self.install("--dry-run", "--with-serve")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn(f"[dry-run] ln -sfn {SYSTEMD}/factory-serve.service", p.stdout)
        self.assertIn("[dry-run] systemctl --user enable factory-serve.service", p.stdout)
        self.assertFalse((self.tmp / "cfg").exists())

    def test_serve_unit_installed_only_with_flag(self):
        link = self.tmp / "cfg" / "systemd" / "user" / "factory-serve.service"
        self.assertEqual(self.install().returncode, 0)
        self.assertFalse(link.is_symlink())
        self.assertNotIn("factory-serve", (self.tmp / "calls").read_text())
        self.assertEqual(self.install("--with-serve").returncode, 0)
        self.assertEqual(link.resolve(), (SYSTEMD / "factory-serve.service").resolve())
        calls = (self.tmp / "calls").read_text()
        self.assertIn("--user enable factory-serve.service", calls)
        self.assertNotIn("--now", calls)
        self.assertNotIn("start", calls)  # nothing is started or restarted

    def test_unknown_flag_exits_2(self):
        p = self.install("--bogus")
        self.assertEqual(p.returncode, 2)
        self.assertIn("--with-serve", p.stderr)

    def test_serve_unit_file_limits(self):
        text = (SYSTEMD / "factory-serve.service").read_text()
        for line in ("ExecStart=%h/factory/bin/factory serve", "Nice=10", "MemoryMax=200M", "MemoryHigh=150M",
                     "CPUQuota=50%", "NoNewPrivileges=yes", "PrivateTmp=yes", "ProtectSystem=strict", "ProtectHome=read-only",
                     "ReadWritePaths=%h/factory", "ReadOnlyPaths=%h/.claude", "WantedBy=default.target",
                     "ReadOnlyPaths=-%h/factory/bin -%h/factory/roles -%h/factory/systemd -%h/factory/web "
                     "-%h/factory/worktrees -%h/factory/.git -%h/factory/tests"):
            self.assertIn(line, text.splitlines())

    def test_install_idempotent_and_safe(self):
        for _ in range(2):
            self.assertEqual(self.install().returncode, 0)
        unit = self.tmp / "cfg" / "systemd" / "user" / "factory-up.service"
        self.assertEqual(unit.resolve(), (SYSTEMD / "factory-up.service").resolve())
        calls = (self.tmp / "calls").read_text()
        self.assertIn("--user enable factory-up.service", calls)
        self.assertNotIn("--now", calls)
        self.assertNotIn("restart", calls)


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
