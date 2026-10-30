"""G-016 T-0352 (M5643): every test that runs the worker-guard hook goes through tests/guard_impl.py, so
GUARD_IMPL=diff|go exercises it against the Go port. Fails naming any module that bypasses the helper."""
import ast
import os
import re
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SELF = "test_guard_impl_coverage"
# modules that only mention the hook path as text (settings templates, docs/policy checks); module -> reason
ALLOWLIST = {
    "test_local_fast_policy": "checks that the coder role text mentions worker-guard",
    "test_risk_tier": "lists roles/hooks/worker-guard.py as a path in a risk-tier fixture",
    "test_scout_roles": "asserts the rendered settings call worker-guard.py with the right --role flag",
    "test_settings_templates": "reads the settings templates; runs ci/guard_diff.py, not the hook",
}
# the guard test modules known at this PR; a new one must use hook_run (and be added here) or go on the allowlist
KNOWN = {
    "test_ci_wait_guard", "test_clean_remote_branch", "test_db_layout", "test_guard_diff", "test_guard_golden",
    "test_guard_factory_cli", "test_guard_impl", "test_guard_probe", "test_worker_guard_units",
    "test_guard_test_sandbox", "test_guard_test_sandbox_pilot", "test_live_home", "test_local_fast_policy",
    "test_priv_guard", "test_risk_tier", "test_scout_roles", "test_settings_templates", "test_skilleval_guard_copy",
    "test_worker_guard", "test_worker_guard_data", "test_worker_guard_git", "test_worker_guard_heredoc",
    "test_worker_guard_persist", "test_worker_guard_public_text", "test_worker_guard_push_pin",
    "test_worker_guard_repocfg", "test_worker_guard_repoprot", "test_worker_guard_reads",
    "test_worker_guard_terms_read", "test_worker_guard_unittest", "test_worker_guard_uv_runner",
}
SPELLING = re.compile(r"worker-guard|worker_guard\.py")


def modules():
    out = {}
    for fn in sorted(os.listdir(HERE)):
        if fn.startswith("test_") and fn.endswith(".py"):
            with open(os.path.join(HERE, fn), encoding="utf-8") as f:
                out[fn[:-3]] = f.read()
    return out


def uses_helper(src):
    for node in ast.walk(ast.parse(src)):
        if isinstance(node, ast.ImportFrom) and node.module == "guard_impl" and any(a.name == "hook_run" for a in node.names):
            return True
        if isinstance(node, ast.Import) and any(a.name == "guard_impl" for a in node.names):
            return True
    return False


def python_only(src):
    for node in ast.parse(src).body:
        if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "GUARD_IMPL_PYTHON_ONLY" for t in node.targets):
            return isinstance(node.value, ast.Constant) and bool(node.value.value)
    return False


def guard_modules(mods):
    return {m for m, src in mods.items() if m != SELF and (SPELLING.search(src) or uses_helper(src) or python_only(src)
                                                  or m.startswith(("test_worker_guard", "test_priv_guard", "test_guard_")))}


class GuardImplCoverage(unittest.TestCase):
    def test_every_guard_test_module_uses_the_helper(self):
        mods = modules()
        bad = sorted(m for m in guard_modules(mods) if not (uses_helper(mods[m]) or python_only(mods[m]) or m in ALLOWLIST)
                     and SPELLING.search(mods[m]))
        self.assertEqual(bad, [], f"{bad} name worker-guard but neither import hook_run from guard_impl, set "
                                  "GUARD_IMPL_PYTHON_ONLY = '<reason>', nor sit in ALLOWLIST (tests/test_guard_impl_coverage.py)")

    def test_allowlist_has_no_stale_entries(self):
        mods = modules()
        stale = sorted(m for m in ALLOWLIST if m not in mods or not SPELLING.search(mods[m]) or uses_helper(mods[m]))
        self.assertEqual(stale, [], f"stale ALLOWLIST entries {stale}: module gone, no longer names worker-guard, or now uses hook_run")

    def test_guard_test_module_set_is_the_known_one(self):
        found = guard_modules(modules())
        self.assertEqual(found, KNOWN, f"guard test modules changed. new={sorted(found - KNOWN)} gone={sorted(KNOWN - found)}; "
                                       "route the new module through guard_impl.hook_run and add it to KNOWN")


if __name__ == "__main__":
    unittest.main()
