"""Tick alert when the installed factory-serve unit drifts from main (T-0263). Temp HOME and FACTORY_HOME only."""
import os
import unittest

from harness import FactoryTestCase

CMDS = ["systemd/install.sh --with-serve", "systemctl --user daemon-reload", "systemctl --user restart factory-serve"]


class ServeUnitDrift(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.set_herdr(agents=[])
        self.main = e.home_dir / "systemd" / "factory-serve.service"
        self.main.parent.mkdir(exist_ok=True)
        self.main.write_text("[Service]\nExecStart=a\n")
        self.inst = e.user_home / ".config" / "systemd" / "user" / "factory-serve.service"

    def install(self, text):
        self.inst.parent.mkdir(parents=True, exist_ok=True)
        self.inst.write_text(text)

    def events(self):
        return list(self.env.sql("SELECT detail FROM events WHERE action='serve-unit-drift' ORDER BY id"))

    def msgs(self):
        return list(self.env.sql("SELECT recipient, body FROM messages WHERE subject LIKE 'factory-serve unit%' ORDER BY id"))

    def test_missing_installed_unit_is_silent(self):
        self.ok("tick")
        self.assertEqual((self.events(), self.msgs()), ([], []))

    def test_equal_is_silent(self):
        self.install(self.main.read_text())
        self.ok("tick")
        self.assertEqual((self.events(), self.msgs()), ([], []))

    def test_drift_alerts_director_once_with_commands(self):
        self.install("[Service]\nExecStart=old\n")
        self.ok("tick")
        self.assertEqual(len(self.events()), 1)
        (m,) = self.msgs()
        self.assertEqual(m["recipient"], "director")
        for c in CMDS:
            self.assertIn(c, m["body"])
        self.assertEqual(self.inst.read_text(), "[Service]\nExecStart=old\n")  # never written

    def test_cooldown_then_new_main_hash_realerts(self):
        self.install("[Service]\nExecStart=old\n")
        self.ok("tick")
        self.ok("tick")
        self.assertEqual((len(self.events()), len(self.msgs())), (1, 1))
        self.main.write_text("[Service]\nExecStart=b\n")
        self.ok("tick")
        self.assertEqual((len(self.events()), len(self.msgs())), (2, 2))


if __name__ == "__main__":
    unittest.main()
