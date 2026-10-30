import json
import os
import re
import subprocess
import unittest

from harness import REPO, FactoryTestCase

SCRIPT = REPO / "roles" / "workflows" / "multi-angle-review.js"
SKILL = REPO / "roles" / "plugins" / "reviewer" / "reviewer-skills" / "skills" / "multi-angle-review" / "SKILL.md"
BRIEF = REPO / "roles" / "plugins" / "lead" / "factory-lead" / "skills" / "worker-brief" / "SKILL.md"
GATED_LINE = "Gated PR: run the multi-angle-review workflow (reviewer skill multi-angle-review) before your verdict."


class WorkflowScriptTests(unittest.TestCase):
    def setUp(self):
        self.src = SCRIPT.read_text()

    def test_meta_is_first_literal(self):
        self.assertTrue(self.src.startswith("export const meta = {"))
        meta = self.src[: self.src.index("\n}\n")]
        self.assertIn("name: 'multi-angle-review'", meta)
        self.assertIn("{ title: 'Review' }", meta)
        self.assertIn("{ title: 'Verify' }", meta)
        self.assertNotIn("${", meta)

    def test_agent_and_parallel_caps(self):
        # 2 agent() call sites: 3 review agents (ANGLES map) + <= 3 verify agents (same ANGLES map)
        self.assertEqual(len(re.findall(r"\bagent\(", self.src)), 2)
        self.assertEqual(len(re.findall(r"key: '", self.src)), 3)
        self.assertEqual(len(re.findall(r"\bparallel\(", self.src)), 2)
        self.assertEqual(re.findall(r"model: '(\w+)'", self.src), ["sonnet", "opus"])
        self.assertIn("phase: 'Review'", self.src)
        self.assertIn("phase: 'Verify'", self.src)

    def test_every_prompt_uses_read_only_rules(self):
        rules = re.search(r"const RULES = `(.*?)`\n", self.src, re.S).group(1)
        for needle in ("must not edit or write any file", "git write commands", "gh pr merge",
                       "gh pr review", "gh pr comment", "must not run any factory command"):
            self.assertIn(needle, rules)
        prompts = re.findall(r"^\s+`\$\{RULES\}", self.src, re.M)
        self.assertEqual(len(prompts), 2)

    def test_returns_contract(self):
        self.assertTrue(re.search(r"return \{\s*confirmed,\s*unverified,\s*dropped:.*angles:", self.src, re.S))

    def test_runs_under_node_syntax_check(self):
        node = subprocess.run(["which", "node"], capture_output=True, text=True)
        if node.returncode:
            self.skipTest("node not installed")
        body = self.src.replace("export const meta", "const meta", 1)
        wrapped = "(async () => {\n" + body + "\n})"
        r = subprocess.run(["node", "--check", "-"], input=wrapped, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)


class WorkflowRunTests(unittest.TestCase):
    """Run the script under node with stubbed agent/parallel/phase globals."""

    def run_script(self, review, verify):
        node = subprocess.run(["which", "node"], capture_output=True, text=True)
        if node.returncode:
            self.skipTest("node not installed")
        body = SCRIPT.read_text().replace("export const meta", "const meta", 1)
        harness = (
            "const review = %s, verify = %s;\n"
            "const phase = () => {};\n"
            "const parallel = fns => Promise.all(fns.map(f => f()));\n"
            "const agent = async (p, o) => o.phase === 'Review' ? review[o.label.split(':')[1]] : verify[o.label.split(':')[1]];\n"
            "const args = {pr: '1', spec_task: 'T-1'};\n"
            "(async () => {\n%s\n})().then(r => console.log(JSON.stringify(r)));\n"
        ) % (json.dumps(review), json.dumps(verify), body)
        r = subprocess.run(["node", "-"], input=harness, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        return json.loads(r.stdout)

    F = {"file": "a.py", "line": 1, "severity": "blocking", "summary": "s", "evidence": "e"}

    def test_null_verifier_leaves_findings_unverified_not_dropped(self):
        review = {"correctness": {"findings": []}, "guard-security": {"findings": [self.F, self.F]},
                  "spec-fit": {"findings": []}}
        out = self.run_script(review, {"guard-security": None})
        self.assertEqual(out["confirmed"], [])
        self.assertEqual(len(out["unverified"]), 2)
        self.assertEqual(out["dropped"], 0)
        angle = [a for a in out["angles"] if a["angle"] == "guard-security"][0]
        self.assertEqual((angle["error"], angle["unverified"]), ("verify", 2))

    def test_null_reviewer_is_an_errored_angle_not_a_clean_one(self):
        review = {"correctness": None, "guard-security": {"findings": []}, "spec-fit": {"findings": []}}
        out = self.run_script(review, {})
        angles = {a["angle"]: a for a in out["angles"]}
        self.assertEqual(angles["correctness"]["error"], "review")
        self.assertNotIn("error", angles["spec-fit"])
        self.assertEqual(out["dropped"], 0)

    def test_healthy_run_confirms_and_drops(self):
        review = {"correctness": {"findings": [self.F, self.F]}, "guard-security": {"findings": []},
                  "spec-fit": {"findings": []}}
        verify = {"correctness": {"verdicts": [{"index": 0, "confirmed": True, "evidence": "x"},
                                               {"index": 1, "confirmed": False, "evidence": "y"}]}}
        out = self.run_script(review, verify)
        self.assertEqual((len(out["confirmed"]), out["dropped"], out["unverified"]), (1, 1, []))


class SkillAndBriefTests(unittest.TestCase):
    def test_skill_states_gate_path_and_verdict(self):
        t = SKILL.read_text()
        for needle in ("roles/hooks/", "roles/settings/", "ci/", ".github/", "> 500",
                       'Workflow(name="reviewer-skills:multi-angle-review"',
                       "write your own `factory verdict", "do not run the workflow"):
            self.assertIn(needle, t)

    def test_lead_brief_line(self):
        t = BRIEF.read_text()
        self.assertIn(GATED_LINE, t)
        self.assertIn("1-agent guard-probe workflow", t)

    def test_reviewer_prompt_pointer_within_budget(self):
        text = (REPO / "roles" / "reviewer.md").read_text()
        pointer = " Gated PR (brief says so): skill `multi-angle-review`."
        self.assertIn(pointer, text)
        self.assertLessEqual(len(pointer.encode()), 120)  # T-0242 byte budget for the one added line

    def test_docs_state_gate_caps_cost_off_switch(self):
        d = (REPO / "docs" / "review-workflow.md").read_text()
        for needle in ("500 changed lines", "at most 6 agents", "at most 3 in parallel", "+250-400k tokens", "Off switch"):
            self.assertIn(needle, d)
        self.assertIn("`reviewer`", (REPO / "docs" / "per-role-plugins.md").read_text())


class ReviewerSpawnTests(FactoryTestCase):
    def test_reviewer_spawn_passes_plugin_dir(self):
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="todo", pr_url=None, team="g-001-a")
        self.ok("spawn", "reviewer", "--name", "zz-test-reviewer", "--goal", "G-001", "--task", "T-0001")
        argv = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]][0]
        i = argv.index("--plugin-dir")
        self.assertEqual(os.path.realpath(argv[i + 1]), str(REPO / "roles" / "plugins" / "reviewer"))


if __name__ == "__main__":
    unittest.main()
