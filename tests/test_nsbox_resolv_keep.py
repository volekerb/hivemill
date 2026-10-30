"""nsbox /run keep-list derived from the real path of resolv.conf (T-0697, acceptance 1 and 2 (a)-(e)).

Contract the implementation must provide: scout.nsbox.run_keep_ro(resolv_path="/etc/resolv.conf") -> tuple of dirs.
It resolves resolv_path with os.path.realpath (looked up as os.path.realpath at call time, so tests can patch it);
if the real path is under /run its parent dir is the only entry, otherwise (); /run itself and anything under
/run/user never come back. No root, no namespaces: pure path logic plus a temp symlink.
"""
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox  # noqa: E402

ETC = "/etc/resolv.conf"


def keep_for(real_path):
    """run_keep_ro(ETC) with os.path.realpath(ETC) pinned to real_path; other realpath calls pass through."""
    fn = getattr(nsbox, "run_keep_ro", None)
    assert fn is not None, "scout.nsbox.run_keep_ro is missing"
    real = os.path.realpath

    def fake(p, *a, **k):
        return real_path if os.fspath(p) == ETC else real(p, *a, **k)

    with mock.patch("os.path.realpath", side_effect=fake):
        return tuple(fn(ETC))


class RunKeepRo(unittest.TestCase):
    def test_function_exists_and_callable(self):
        self.assertTrue(callable(getattr(nsbox, "run_keep_ro", None)), "scout.nsbox.run_keep_ro must exist")

    def test_a_systemd_resolved_stub(self):
        self.assertEqual(keep_for("/run/systemd/resolve/stub-resolv.conf"), ("/run/systemd/resolve",))

    def test_b_resolvconf(self):
        self.assertEqual(keep_for("/run/resolvconf/resolv.conf"), ("/run/resolvconf",))

    def test_c_network_manager(self):
        self.assertEqual(keep_for("/run/NetworkManager/resolv.conf"), ("/run/NetworkManager",))

    def test_d_plain_etc_file_keeps_nothing(self):
        self.assertEqual(keep_for("/etc/resolv.conf"), ())

    def test_d2_other_non_run_target_keeps_nothing(self):
        self.assertEqual(keep_for("/var/lib/dns/resolv.conf"), ())

    def test_d3_run_prefix_lookalike_is_not_under_run(self):
        self.assertEqual(keep_for("/runner/dns/resolv.conf"), ())

    def test_e_file_directly_in_run_keeps_nothing(self):
        # parent would be /run itself: never re-expose the whole mask
        self.assertEqual(keep_for("/run/resolv.conf"), ())

    def test_e2_under_run_user_keeps_nothing(self):
        uid = os.getuid()
        for p in (f"/run/user/{uid}/resolv.conf", f"/run/user/{uid}/systemd/resolve/resolv.conf",
                  "/run/user/1000/resolv.conf", "/run/user/resolv.conf"):
            with self.subTest(path=p):
                self.assertEqual(keep_for(p), ())

    def test_only_one_entry_and_nothing_else_under_run(self):
        got = keep_for("/run/systemd/resolve/stub-resolv.conf")
        self.assertEqual(len(got), 1)
        self.assertNotIn("/run", got)
        self.assertFalse([k for k in got if k.startswith("/run/user")])

    def test_returns_tuple(self):
        fn = getattr(nsbox, "run_keep_ro", None)
        self.assertIsNotNone(fn, "scout.nsbox.run_keep_ro must exist")
        self.assertIsInstance(keep_for("/run/resolvconf/resolv.conf"), tuple)

    def test_real_symlink_chain_is_followed(self):
        fn = getattr(nsbox, "run_keep_ro", None)
        self.assertIsNotNone(fn, "scout.nsbox.run_keep_ro must exist")
        with tempfile.TemporaryDirectory(prefix="zz-test-resolv-keep-") as d:
            target = Path(d).resolve() / "resolv.conf"
            target.write_text("nameserver 127.0.0.1\n")
            link = Path(d).resolve() / "link"
            link.symlink_to(target)
            # injected path is a symlink to a file outside /run: realpath resolves it, nothing is kept
            self.assertEqual(tuple(fn(str(link))), ())

    def test_hardcoded_systemd_entry_gone_from_constant(self):
        self.assertNotIn("/run/systemd/resolve", tuple(getattr(nsbox, "RUN_KEEP_RO", ())),
                         "RUN_KEEP_RO must not hardcode /run/systemd/resolve")
        self.assertTrue(callable(getattr(nsbox, "run_keep_ro", None)), "scout.nsbox.run_keep_ro must exist")


if __name__ == "__main__":
    unittest.main()
