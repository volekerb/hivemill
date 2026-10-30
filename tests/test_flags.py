"""bin/flags.json registers every DEFAULT_CONFIG key (T-0793); `factory flags` lists it (docs/RELEASING.md#feature-flags)."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from harness import REPO, FactoryTestCase, _literal  # noqa: E402

REGISTRY = REPO / "bin" / "flags.json"
CONFIG = _literal("DEFAULT_CONFIG")
ENTRIES = json.loads(REGISTRY.read_text())["keys"]
KINDS = {"flag", "setting"}
STATES = {"off", "shadow", "on", "removed"}
FLAGS = {
    "lead_parking", "stale_policy", "refresh_lane", "lead_less_dispatch", "review_autospawn", "prcheck_required",
    "git_push", "state_push", "mech_merge", "risk_tiers", "red_main_freeze", "gate_sandbox", "test_sandbox",
    "test_cgroup", "test_sandbox_subuid", "gate_post_status", "sandbox_roles", "worker_gh_roles", "task_clones",
    "tick_async_start", "lead_wake_debounce_s", "tick_phase_timers", "orphan_reap_mode", "scout_enabled",
    "gate_fast_projects", "issue_intake_repos",
}
BASELINE_ON = {"git_push", "stale_policy", "scout_enabled", "gate_fast_projects"}


def live(e):
    return e.get("state") != "removed"


class Registry(unittest.TestCase):
    def test_every_config_key_is_registered(self):
        missing = sorted(set(CONFIG) - {e["key"] for e in ENTRIES if live(e)})
        extra = sorted({e["key"] for e in ENTRIES if live(e)} - set(CONFIG))
        self.assertEqual(missing, [], "config key(s) without a registry entry: " + ", ".join(missing)
                         + " - add an entry to bin/flags.json (docs/RELEASING.md#feature-flags)")
        self.assertEqual(extra, [], f"registry key(s) not in DEFAULT_CONFIG: {extra}; remove them or set state removed")

    def test_defaults_match(self):
        for e in ENTRIES:
            if live(e):
                self.assertEqual(e["default"], CONFIG[e["key"]], f"{e['key']}: default differs from DEFAULT_CONFIG")

    def test_enums_and_required_fields(self):
        for e in ENTRIES:
            self.assertIn(e["kind"], KINDS, e["key"])
            for f in ("key", "default", "summary", "goal", "doc"):
                self.assertIsInstance(e.get(f), str, f"{e['key']}: {f}")
            self.assertTrue(e["summary"], f"{e['key']}: empty summary")
            if e["kind"] == "flag":
                self.assertIn(e.get("state"), STATES, e["key"])
            else:
                self.assertNotIn("state", e, f"{e['key']}: settings carry no state")

    def test_on_flags_have_promoted_in(self):
        for e in ENTRIES:
            if e["kind"] == "flag" and e["state"] == "on":
                self.assertRegex(e.get("promoted_in", ""), r"^(baseline|\d+\.\d+\.\d+)$", e["key"])

    def test_removed_keys_are_not_config(self):
        for e in ENTRIES:
            if e.get("state") == "removed":
                self.assertNotIn(e["key"], CONFIG, f"{e['key']} is removed but still in DEFAULT_CONFIG")

    def test_sorted_without_duplicates(self):
        keys = [e["key"] for e in ENTRIES]
        self.assertEqual(keys, sorted(keys))
        self.assertEqual(len(keys), len(set(keys)), "duplicate registry keys")

    def test_exact_flag_list_and_states(self):
        self.assertEqual({e["key"] for e in ENTRIES if e["kind"] == "flag"}, FLAGS)
        by = {e["key"]: e for e in ENTRIES}
        for k in FLAGS:
            if k in BASELINE_ON:
                self.assertEqual((by[k]["state"], by[k]["promoted_in"]), ("on", "baseline"), k)
            elif k == "orphan_reap_mode":
                self.assertEqual(by[k]["state"], "shadow")
            else:
                self.assertEqual(by[k]["state"], "off", k)

    def test_unregistered_key_is_reported(self):
        """C4: the message of the coverage check names the key and says what to do."""
        fake = dict(CONFIG, zz_fake_key="0")
        missing = sorted(set(fake) - {e["key"] for e in ENTRIES if live(e)})
        self.assertEqual(missing, ["zz_fake_key"])


class FlagsCommand(FactoryTestCase):
    def snapshot(self):
        con = sqlite3.connect(self.env.db_path)
        try:
            return {t: con.execute(f"SELECT * FROM {t}").fetchall() for t in ("config", "agents", "events", "messages")}
        finally:
            con.close()

    def test_default_lists_flags_only(self):
        before = self.snapshot()
        p = self.ok("flags", agent="owner")
        lines = p.stdout.splitlines()
        self.assertEqual(lines[0].split(), ["KEY", "STATE", "DEFAULT", "CURRENT", "GOAL", "DOC"])
        self.assertEqual(sorted(l.split()[0] for l in lines[1:]), sorted(FLAGS))
        row = next(l.split() for l in lines if l.startswith("git_push "))
        self.assertEqual(row[:4], ["git_push", "on", "1", "0"])  # the harness turns git_push off on the temp home
        self.assertEqual(before, self.snapshot())

    def test_any_role_may_run_it(self):
        self.ok("flags", agent="coder-x")

    def test_all_lists_every_entry(self):
        lines = self.ok("flags", "--all", agent="owner").stdout.splitlines()
        self.assertEqual(len(lines) - 1, len(ENTRIES))
        self.assertEqual(next(l.split()[1] for l in lines if l.startswith("serve_port ")), "-")

    def test_json_has_current(self):
        data = json.loads(self.ok("flags", "--json", agent="owner").stdout)
        self.assertEqual(len(data), len(ENTRIES))
        self.assertEqual(next(e for e in data if e["key"] == "serve_port")["current"], CONFIG["serve_port"])
        self.assertEqual(next(e for e in data if e["key"] == "git_push")["current"], "0")

    def test_bad_registry_exits_2(self):
        # a copy of bin/ with an invalid registry: the real entry point reads flags.json next to factory_cli.py
        root = self.env.home / "repocopy"
        for d in ("bin", "web", "scout"):
            shutil.copytree(REPO / d, root / d, ignore=shutil.ignore_patterns("__pycache__"))
        (root / "bin" / "flags.json").write_text("{not json")
        p = subprocess.run([sys.executable, str(root / "bin" / "factory"), "flags"], capture_output=True, text=True,
                           env=self.env.env("owner"), cwd=self.env.home, timeout=60)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertTrue(p.stderr.startswith("factory flags: cannot read bin/flags.json:"), p.stderr)


if __name__ == "__main__":
    unittest.main()
