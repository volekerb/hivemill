import json
import re
import unittest
from pathlib import Path

from harness import rendered_settings_dir

SETTINGS = sorted(rendered_settings_dir().glob("*.json"))
# Claude Code skips a rule whose tool name is not capitalised and shows a blocking Settings Warning at start-up.
RULE = re.compile(r"^[A-Z][A-Za-z]*(\(|$)")


def bad_rules(d):
    perms = d.get("permissions", {})
    rules = [r for key in ("allow", "deny") for r in perms.get(key, [])]
    return [r for r in rules if not r.startswith("mcp__") and not RULE.match(r)]


class RoleSettingsRuleNameTests(unittest.TestCase):
    def test_every_allow_and_deny_rule_starts_with_an_uppercase_tool_name(self):
        self.assertGreaterEqual(len(SETTINGS), 12)
        for p in SETTINGS:
            self.assertEqual(bad_rules(json.loads(p.read_text())), [], p.name)

    def test_checker_rejects_lowercase_and_accepts_valid_rules(self):
        self.assertEqual(bad_rules({"permissions": {"deny": ["advisor", "bash(ls)"], "allow": ["read"]}}),
                         ["read", "advisor", "bash(ls)"])
        ok = {"permissions": {"deny": ["Advisor", "Bash(sudo:*)"], "allow": ["Read", "mcp__x__y"]}}
        self.assertEqual(bad_rules(ok), [])


def load(role):
    return json.loads((rendered_settings_dir() / f"{role}.json").read_text())


def allow(role):
    return load(role)["permissions"]["allow"]


def owner_cap_keys():
    src = (Path(__file__).resolve().parent.parent / "bin" / "factory").read_text()
    m = re.search(r"^OWNER_CAP_KEYS = \((.*?)\)", src, re.S | re.M)
    return re.findall(r'"(\w+)"', m.group(1))


class AllowListTests(unittest.TestCase):
    def test_l1_rules_present(self):
        for rule in ("Bash(factory spawn cleaner)", "Bash(factory spawn cleaner *)", "Bash(factory spawn coder *)",
                     "Bash(factory project update * --gh *)", "Bash(factory config nudge_cooldown_s *)",
                     "Bash(factory stop *)", "Bash(factory judge * --record-merged *)", "Bash(factory relaunch *)"):
            self.assertIn(rule, allow("director"))
        for rule in ("Bash(factory judge * --merge *)", "Bash(factory judge * --reject *)",
                     "Bash(git merge-base --is-ancestor origin/main *)", "Bash(git fetch origin)"):
            self.assertIn(rule, allow("judge"))
        for rule in ("Bash(factory clean)", "Bash(factory clean --dry-run)", "Bash(factory clean --remote-branch factory/*)",
                     "Bash(git worktree list)", "Bash(df -h *)", "Bash(du -sh *)"):
            self.assertIn(rule, allow("cleaner"))

    def test_auto_mode_block_keeps_defaults(self):
        for role in ("director", "judge", "cleaner"):
            am = load(role)["autoMode"]
            self.assertEqual(am["environment"][0], "$defaults", role)
            self.assertEqual(am["allow"][0], "$defaults", role)
            self.assertGreaterEqual(len(am["allow"]), 2, role)

    def test_director_asks_on_host_steering_project_flags(self):
        ask = load("director")["permissions"]["ask"]
        # argparse accepts --repo=x and unique prefixes (--rep, --gate-cm), so the patterns are prefix globs
        for flag in ("--gate-cmd*", "--gate-count-cmd*", "--gate-c*", "--rep*"):
            self.assertIn(f"Bash(factory project * {flag})", ask)

    def test_no_rm_allow_rule_and_no_owner_cap_config_rule(self):
        keys = owner_cap_keys()
        self.assertIn("max_live_judges", keys)
        for p in SETTINGS:
            for rule in json.loads(p.read_text())["permissions"]["allow"]:
                self.assertFalse(rule.startswith("Bash(rm"), (p.name, rule))
                for k in keys:
                    self.assertNotIn(f"factory config {k}", rule, (p.name, rule))
                self.assertNotEqual(rule, "Bash(factory config *)", p.name)

    def test_coder_push_and_pr_rules_are_narrow(self):
        rules = [r for r in allow("coder") if re.match(r"Bash\((git push|gh pr (create|ready))", r)]
        self.assertEqual(sorted(rules), sorted([
            "Bash(git push origin HEAD:factory/*)",
            "Bash(gh pr create --repo volekerb/hivemill *)",
            "Bash(gh pr ready --repo volekerb/hivemill *)",
            "Bash(gh pr ready * --repo volekerb/hivemill)"]))
        self.assertEqual([r for r in allow("coder") if re.match(r"Bash\((git push|gh pr (merge|close|edit|comment))", r)
                          and not r.startswith(("Bash(git push origin HEAD:factory/", "Bash(gh pr create", "Bash(gh pr ready"))], [])

    def test_coder_push_denies_extra_args_after_the_factory_refspec(self):
        # allow globs span spaces: the deny must catch a second refspec or flag after HEAD:factory/<x>
        self.assertIn("Bash(git push origin HEAD:factory/* *)", load("coder")["permissions"]["deny"])
        self.assertNotIn("Bash(git push origin HEAD:factory/* *)", allow("coder"))

    def test_other_worker_roles_gain_no_push_or_pr_create_rule(self):
        for role in ("pm", "qa", "reviewer", "judge", "director", "lead"):
            for r in allow(role):
                self.assertNotRegex(r, r"git push|gh pr (create|ready)", role)

    def test_every_rendered_allow_and_deny_rule_name_is_valid_with_ask_present(self):
        for p in SETTINGS:
            d = json.loads(p.read_text())
            self.assertEqual(bad_rules({"permissions": {"ask": [], **d["permissions"]}}), [], p.name)


if __name__ == "__main__":
    unittest.main()
