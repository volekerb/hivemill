import re
import subprocess
import sys
import unittest

from harness import FACTORY, VALID_PR_BODY, FactoryTestCase, pr_body

BASE_WORDS = len(re.sub(r"<details.*?</details>", "", pr_body(), flags=re.S).split())  # words outside <details> at pad 0
URL = "https://github.com/o/r/pull/7"
TEMPLATE = ".github/pull_request_template.md"


class PrBodyTemplateTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="lead-g-001-a")
        e.seed_task("T-0001", status="in_progress", pr_url=None, team="g-001-a")
        e.seed_agent("coder-t-0001", "coder", goal="G-001", team="g-001-a", task="T-0001")

    def status(self):
        return self.env.sql("SELECT status, pr_url FROM tasks WHERE id='T-0001'")[0]

    def in_review(self, body):
        self.env.seed_pr(url=URL, body=body)
        return self.env.run("task", "update", "T-0001", "--status", "in_review", "--pr", URL, agent="coder-t-0001")

    def assertRefused(self, p, *needles):
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn(TEMPLATE, p.stderr)
        for n in needles:
            self.assertIn(n, p.stderr)
        self.assertEqual(self.status()["status"], "in_progress")
        self.assertIsNone(self.status()["pr_url"])

    def test_valid_body_accepted_without_warning(self):
        p = self.in_review(pr_body(200))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertNotIn("words outside", p.stderr)
        self.assertEqual(self.status()["status"], "in_review")

    def test_missing_heading_refused(self):
        p = self.in_review(VALID_PR_BODY.replace("## Changes\n", "Changes\n"))
        self.assertRefused(p, "missing section heading", "## Changes")

    def test_wrong_order_refused(self):
        body = VALID_PR_BODY.replace("## Why\n", "## TMP\n").replace("## Summary\n", "## Why\n").replace("## TMP\n", "## Summary\n")
        self.assertRefused(self.in_review(body), "out of order")

    def test_every_problem_listed(self):
        p = self.in_review("## Summary\nonly this\n")
        self.assertRefused(p, "## Why", "## Risk / rollback", "no <details> block")

    def test_walk_outside_details_refused(self):
        body = VALID_PR_BODY.replace("<details><summary>Acceptance walk</summary>", "").replace("</details>", "")
        self.assertRefused(self.in_review(body), "no <details> block")

    def test_walk_in_other_details_refused(self):
        body = VALID_PR_BODY.replace("<summary>Acceptance walk</summary>", "<summary>x</summary>").replace(
            "## Acceptance walk", "## Other") + "\n## Acceptance walk\n1. outside\n"
        self.assertRefused(self.in_review(body), "no <details> block")

    def test_empty_walk_refused(self):
        body = VALID_PR_BODY.replace("1. met - cmd -> out\n", "\n")
        self.assertRefused(self.in_review(body), "no non-empty line")

    def test_301_words_refused(self):
        self.assertRefused(self.in_review(pr_body(301 - BASE_WORDS)), "301 words")

    def test_280_words_allowed_with_warning(self):
        p = self.in_review(pr_body(280 - BASE_WORDS))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("PR body 280 words outside <details>; template target 150-250", p.stderr)
        self.assertEqual(self.status()["status"], "in_review")

    def test_details_and_comments_not_counted(self):
        body = pr_body(200) + "<details><summary>audit</summary>\n" + "x " * 500 + "\n</details>\n<!-- " + "y " * 500 + " -->\n"
        p = self.in_review(body)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertNotIn("words outside", p.stderr)

    def test_gh_error_refused(self):
        p = self.env.run("task", "update", "T-0001", "--status", "in_review", "--pr", URL, agent="coder-t-0001")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("cannot read the body", p.stderr)
        self.assertEqual(self.status()["status"], "in_progress")

    def write_template(self, root, text):
        tpl = root / TEMPLATE
        tpl.parent.mkdir(parents=True, exist_ok=True)
        tpl.write_text(text)

    def test_extra_template_headings_added_to_builtin(self):
        self.write_template(self.env.home_dir, "<!-- x -->\n## Summary\n\n## Alpha\n\n<details>\n## Acceptance walk\n</details>\n")
        p = self.in_review(VALID_PR_BODY)  # the template's `## Alpha` is required too
        self.assertRefused(p, "## Alpha")
        body = VALID_PR_BODY.replace("## Why", "## Alpha\na\n\n## Why", 1)
        self.assertEqual(self.in_review(body).returncode, 0)

    def test_shortened_template_cannot_weaken_check(self):
        self.write_template(self.env.home_dir, "## Summary\n")
        p = self.in_review("## Summary\ns\n\n" + VALID_PR_BODY[VALID_PR_BODY.index("<details>"):])
        self.assertRefused(p, "## Why", "## Changes", "## Test evidence", "## Risk / rollback")

    def test_worktree_template_is_ignored(self):
        wt = self.env.home / "worktree"
        self.write_template(wt, "## Summary\n## Why\n")  # 2-heading template in the coder's cwd
        self.env.seed_pr(url=URL, body="## Summary\ns\n\n## Why\nw\n\n" + VALID_PR_BODY[VALID_PR_BODY.index("<details>"):])
        p = subprocess.run([sys.executable, str(FACTORY), "task", "update", "T-0001", "--status", "in_review", "--pr", URL],
                           capture_output=True, text=True, env=self.env.env("coder-t-0001", None), cwd=wt, timeout=60)
        self.assertRefused(p, "## Changes", "## Test evidence", "## Risk / rollback")

    def test_worktree_template_extra_heading_not_required(self):
        wt = self.env.home / "worktree"
        self.write_template(wt, "## Zeta\n")
        self.env.seed_pr(url=URL, body=VALID_PR_BODY)
        p = subprocess.run([sys.executable, str(FACTORY), "task", "update", "T-0001", "--status", "in_review", "--pr", URL],
                           capture_output=True, text=True, env=self.env.env("coder-t-0001", None), cwd=wt, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_fallback_headings_without_template_file(self):
        self.assertFalse((self.env.home_dir / TEMPLATE).exists())
        p = self.in_review("## Alpha\na\n\n## Beta\nb\n\n" + VALID_PR_BODY[VALID_PR_BODY.index("<details>"):])
        self.assertRefused(p, "## Summary", "## Risk / rollback")


if __name__ == "__main__":
    unittest.main()
