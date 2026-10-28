"""Layer 3 of the worker guard: bin/factory herdr() pins HERDR_SOCKET_PATH for its own herdr calls."""
import os

from harness import FactoryTestCase

BOGUS = "/proc/nonexistent/worker-no-herdr.sock"


class HerdrSocketTests(FactoryTestCase):
    def socks(self):
        return {c["sock"] for c in self.env.calls("herdr")}

    def test_default_socket_in_a_systemd_like_env(self):
        # systemd user units run with a minimal env: no HERDR_*, no CLAUDECODE, only HOME and PATH
        e = self.env
        self.ok("res")
        self.assertTrue(e.calls("herdr"))
        self.assertEqual(self.socks(), {os.path.join(str(e.user_home), ".config/herdr/herdr.sock")})

    def test_caller_env_cannot_redirect_the_call(self):
        e = self.env
        e.run("res", extra_env={"HERDR_SOCKET_PATH": BOGUS, "HERDR_ENV": "1"})
        self.assertTrue(e.calls("herdr"))
        self.assertEqual(self.socks(), {os.path.join(str(e.user_home), ".config/herdr/herdr.sock")})

    def test_configured_socket(self):
        e = self.env
        e.run("config", "herdr_socket", "/run/user/1000/custom.sock")
        e.run("res", extra_env={"HERDR_SOCKET_PATH": BOGUS})
        self.assertEqual(self.socks(), {"/run/user/1000/custom.sock"})

    def test_config_lists_herdr_socket(self):
        p = self.ok("config")
        self.assertIn("herdr_socket", p.stdout)
        self.assertIn("~/.config/herdr/herdr.sock", p.stdout)
