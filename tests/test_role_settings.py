import json
import re
import unittest

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


if __name__ == "__main__":
    unittest.main()
