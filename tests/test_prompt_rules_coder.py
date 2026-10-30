import json
import os
import re
import subprocess
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
from pathlib import Path
SCRIPT = Path(ROOT) / "roles" / "workflows" / "multi-angle-review.js"


def read(name):
    with open(os.path.join(ROOT, "roles", name), encoding="utf-8") as f:
        return f.read()


CODER_MARKERS = {
    "C1": "Tests assert outcomes",
    "C2": "Tests are deterministic",
    "C3": "ships a test it must reject and one it must accept",
    "C4": "with the fix removed and see it fail",
    "C5": "0 tests, or fewer than main",
    "C6": "Tests never touch the live home",
    "C7": "A check that cannot decide fails or reports unknown",
    "C8": "`follow-ups:` lines",
    "C9": "dollar-paren sequence",
    "C10": "python3 -m unittest discover -s tests -p test_X.py",
    "G013": "pre-pr-check",
    "G013C1": "self-review",
}
K_MARKERS = {
    "K1": "No machine-level changes",
    "K2": "unless the owner named both that repo and that action",
}
KEPT_COMMANDS = [
    "factory task update <T> --status in_progress",
    "git push origin HEAD:factory/<task-id>",
    "gh pr create",
    "factory task update <T> --status in_review --pr",
    "factory done",
    "coder-skills:ci-wait",
    "docs/<feature>.md",
    "## Standing rules",
]


class PromptRulesCoder(unittest.TestCase):
    def test_coder_rules(self):
        text = read("coder.md")
        for rid, marker in CODER_MARKERS.items():
            self.assertIn(marker, text, "rule %s missing from coder.md" % rid)

    def test_k_rules_in_coder_and_qa(self):
        for name in ("coder.md", "qa.md"):
            text = read(name)
            for rid, marker in K_MARKERS.items():
                self.assertIn(marker, text, "rule %s missing from %s" % (rid, name))

    def test_common_has_no_k_rules(self):
        text = read("_common.md")
        for rid, marker in K_MARKERS.items():
            self.assertNotIn(marker, text, "rule %s must not be in _common.md" % rid)

    def test_qa_keeps_rules(self):
        text = read("qa.md")
        for rid in ("Q1", "Q2", "Q3", "Q4", "Q5"):
            self.assertIn("(%s)" % rid, text, "qa.md lost %s" % rid)
        self.assertIn("## Gates", text, "qa.md lost the Gates instruction")

    def test_coder_keeps_commands(self):
        text = read("coder.md")
        for cmd in KEPT_COMMANDS:
            self.assertIn(cmd, text, "coder.md lost command: %s" % cmd)

    def test_self_review_skill(self):
        path = os.path.join(
            ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "self-review", "SKILL.md"
        )
        self.assertTrue(os.path.isfile(path), "self-review SKILL.md missing")
        with open(path, encoding="utf-8") as f:
            text = f.read()
        self.assertTrue(text.startswith("---\nname: self-review\n"), "front matter name")
        self.assertIn("Use when", text.split("---")[1])
        for marker in ("sonnet", "roles/reviewer.md", "Self-review: N findings", "advisor"):
            self.assertIn(marker, text, "skill lacks %s" % marker)
        self.assertIn("Never use opus or fable", text, "skill lacks the opus/fable bar")
        self.assertIn("never run a second subagent", text, "skill lacks the second-subagent bar")
        for line in text.splitlines():
            if "opus" in line.lower() or "fable" in line.lower():
                self.assertIn("Never", line, "opus/fable outside a never sentence: %s" % line)
        self.assertLess(len(text.splitlines()), 70)

    def test_self_review_subagent_read_only(self):
        path = os.path.join(
            ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "self-review", "SKILL.md"
        )
        with open(path, encoding="utf-8") as f:
            text = f.read()
        self.assertIn("Tell it explicitly", text, "skill lacks the explicit bar sentence")
        text = text.split("Tell it explicitly", 1)[1].split("\n", 1)[0]
        for barred in (
            "never edit files",
            "never run git writes (commit, push, merge, checkout, reset, stash)",
            "gh pr merge",
            "gh pr review",
            "gh pr comment",
            "gh pr edit",
            "never run any `factory` command",
        ):
            self.assertIn(barred, text, "read-only subagent bar lacks: %s" % barred)

    def test_self_review_step_order(self):
        text = read("coder.md")
        pre = text.index("pre-pr-check")
        sr = text.index("coder-skills:self-review")
        inrev = text.index("--status in_review")
        self.assertLess(pre, sr)
        self.assertLess(sr, inrev)
        self.assertLess(text.index("## Steps"), pre)


    def _skill(self):
        path = os.path.join(
            ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "self-review", "SKILL.md"
        )
        with open(path, encoding="utf-8") as f:
            return f.read()

    def test_self_review_gate_list_and_three_angle_workflow(self):
        text = self._skill()
        step = text.split("\n6. ", 1)[1].split("\n\n", 1)[0]
        self.assertIn('Workflow(name="coder-skills:multi-angle-review"', step)
        for item in ("roles/hooks/", "roles/settings/", "ci/", ".github/", "scout/nsbox.py",
                     "scout/workerbox.py", "500", "def cmd_judge", "def refresh_branch",
                     "def cmd_refresh", "def head_has_main", "def verdict_carries"):
            self.assertIn(item, step, "gate list lacks %s" % item)
            self.assertEqual(text.count(item), 1, "gate item written more than once: %s" % item)
        for marker in ("sonnet", "3 agents", "no verifiers", "(3-angle)", '"mode": "coder"'):
            self.assertIn(marker, step, "step 6 lacks %s" % marker)
        for line in text.splitlines():
            if "opus" in line.lower() or "fable" in line.lower():
                self.assertIn("Never", line, "opus/fable outside a never sentence")

    def test_workflow_fallback_marks_errored_slot(self):
        js = SCRIPT.read_text(encoding="utf-8")
        fb = [l for l in js.splitlines() if l.startswith("const results = checked.map")][0]
        self.assertIn("errored: true", fb)
        self.assertIn("found: null", fb)
        self.assertNotRegex(fb, r"found: 0[^}]*\}\)\s*$")
        self.assertIn("errored: results.filter", js)
        self.assertIn("lands in the final fallback", js)

    def test_workflow_rules_role_neutral(self):
        js = SCRIPT.read_text(encoding="utf-8")
        rules = [l for l in js.splitlines() if l.startswith("const RULES")][0]
        self.assertNotIn("reviewer's worktree", rules)
        self.assertIn("the current worktree", rules)

    def test_reviewer_skill_hand_reviews_any_errored_angle(self):
        path = os.path.join(ROOT, "roles", "plugins", "reviewer", "reviewer-skills",
                            "skills", "multi-angle-review", "SKILL.md")
        with open(path, encoding="utf-8") as f:
            text = f.read()
        line = [l for l in text.splitlines() if l.startswith("5. ")][0]
        for marker in ("any `error`", '"review"', '"verify"', "fallback", "hand-review"):
            self.assertIn(marker, line)

    def test_self_review_requires_open_pr(self):
        step = self._skill().split("\n6. ", 1)[1].split("\n\n", 1)[0]
        self.assertIn("must already be open", step)
        self.assertIn("before `factory task update <T> --status in_review`", step)

    def _run_script(self, args, review):
        node = subprocess.run(["which", "node"], capture_output=True, text=True)
        if node.returncode:
            self.skipTest("node not installed")
        body = SCRIPT.read_text(encoding="utf-8").replace("export const meta", "const meta", 1)
        harness = (
            "const review = %s, args = %s, calls = [];\n"
            "const phase = () => {};\n"
            "const parallel = fns => Promise.all(fns.map(f => f()));\n"
            "const agent = async (p, o) => { calls.push({label: o.label, model: o.model, p});"
            " return review[o.label.split(':')[1]]; };\n"
            "(async () => {\n%s\n})().then(r => console.log(JSON.stringify({r, calls})));\n"
        ) % (json.dumps(review), json.dumps(args), body)
        r = subprocess.run(["node", "-"], input=harness, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        return json.loads(r.stdout)

    F = {"file": "a.py", "line": 1, "severity": "blocking", "summary": "s", "evidence": "e"}

    def test_workflow_coder_mode_three_sonnet_agents_no_verifier(self):
        review = {"security": {"findings": [self.F]}, "spec": {"findings": []}, "tests": None}
        out = self._run_script(
            {"mode": "coder", "pr": "http://x/1", "task": "T-1", "acceptance": "ACC-TEXT-7"}, review)
        calls = out["calls"]
        self.assertEqual(sorted(c["label"] for c in calls),
                         ["review:security", "review:spec", "review:tests"])
        self.assertEqual({c["model"] for c in calls}, {"sonnet"})
        self.assertTrue(all("ACC-TEXT-7" in c["p"] for c in calls), "acceptance not passed to agents")
        r = out["r"]
        self.assertEqual((r["confirmed"], len(r["unverified"]), r["dropped"]), ([], 1, 0))
        angles = {a["angle"]: a for a in r["angles"]}
        self.assertEqual(angles["tests"]["error"], "review")
        self.assertNotIn("error", angles["spec"])

    def test_workflow_default_path_unchanged(self):
        src = SCRIPT.read_text(encoding="utf-8")
        # constants copied from origin/main at T-0242's merge (462dda34)
        self.assertEqual(re.findall(r"model: '(\w+)'", src), ["sonnet", "opus"])
        self.assertEqual(len(re.findall(r"\bagent\(", src)), 2)
        self.assertEqual(len(re.findall(r"\bparallel\(", src)), 2)
        self.assertEqual(len(re.findall(r"key: '", src)), 3)
        review = {"correctness": {"findings": []}, "guard-security": {"findings": []},
                  "spec-fit": {"findings": []}}
        out = self._run_script({"pr": "http://x/1", "spec_task": "T-1"}, review)
        self.assertEqual(sorted(c["label"] for c in out["calls"]),
                         ["review:correctness", "review:guard-security", "review:spec-fit"])
        self.assertEqual({c["model"] for c in out["calls"]}, {"sonnet"})


if __name__ == "__main__":
    unittest.main()
