import unittest

from harness import SHA_A, FactoryTestCase, gates_summary

GOOD = "## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n- compile exit=0 cmd=python3 -m py_compile bin/factory\n"
FORMAT = "Gate line format:"


class VerdictGatesTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        e.seed_pr()

    def counts(self):
        return (self.env.sql("SELECT count(*) c FROM verdicts")[0]["c"], self.env.sql("SELECT count(*) c FROM events")[0]["c"])

    def assert_refused(self, summary, *extra, agent="zz-test-qa", reason=None):
        before = self.counts()
        p = self.refused(5, "verdict", "T-0001", "--pass", "--summary", summary, *extra, agent=agent)
        self.assertIn(FORMAT, p.stdout + p.stderr)
        if reason:
            self.assertIn(reason, p.stdout + p.stderr)
        self.assertEqual(self.counts(), before)

    def test_qa_pass_with_gates_recorded(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "all ok\n\n" + GOOD, agent="zz-test-qa")
        row = self.env.sql("SELECT * FROM verdicts")[0]
        self.assertEqual((row["head_sha"], row["role"], row["verdict"]), (SHA_A, "qa", "pass"))

    def test_qa_pass_no_section(self):
        self.assert_refused("ok", reason="missing")

    def test_qa_pass_empty_section(self):
        self.assert_refused("ok\n## Gates\n\n## Other\n- x exit=0 cmd=y\n", reason="no gate lines")

    def test_qa_pass_malformed_line(self):
        self.assert_refused("## Gates\n- unittest ok cmd=x\n", reason="line 2 malformed")

    def test_qa_pass_duplicate_name(self):
        self.assert_refused(GOOD + "- unittest exit=0 cmd=again\n", reason="duplicate name unittest")

    def test_qa_pass_nonzero_exit(self):
        self.assert_refused("## Gates\n- unittest exit=1 cmd=x\n", reason="gate unittest exit=1")

    def test_qa_pass_unknown_exit(self):
        self.assert_refused("## Gates\n- unittest exit=unknown cmd=x\n", reason="exit=unknown")

    def test_qa_pass_two_sections(self):
        self.assert_refused(GOOD + "\n## Gates\n- lint exit=0 cmd=ruff\n", reason="2 '## Gates' sections")

    def test_qa_fail_without_section_recorded(self):
        self.ok("verdict", "T-0001", "--fail", "--summary", "broken", agent="zz-test-qa")
        self.assertEqual(self.counts()[0], 1)

    def test_reviewer_pass_without_section_recorded(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "lgtm", agent="zz-test-reviewer")
        self.assertEqual(self.env.sql("SELECT role FROM verdicts")[0]["role"], "reviewer")

    def test_reviewer_pass_with_valid_section_recorded(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", GOOD, agent="zz-test-reviewer")

    def test_reviewer_pass_malformed_section_refused(self):
        self.assert_refused("lgtm\n## Gates\n- bad line\n", agent="zz-test-reviewer", reason="malformed")

    def test_owner_as_qa_follows_qa_rules(self):
        self.assert_refused("ok", "--as", "qa", agent="owner")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), "--as", "qa", agent="owner")

    def test_owner_as_reviewer_optional(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", "--as", "reviewer", agent="owner")
        self.assert_refused("## Gates\n- x exit=2 cmd=y\n", "--as", "reviewer", agent="owner")

    # -- parse_gates unit cases -----------------------------------------
    def test_parse_ends_at_next_heading_and_ignores_blank(self):
        with self.env.load_factory() as mod:
            text = "intro\n## Gates\n\n- a exit=0 cmd=x\n\n- b exit=-1 cmd=y\n## Notes\n- not a gate\n"
            sections, gates, errors = mod.parse_gates(text)
        self.assertEqual((sections, errors), (1, []))
        self.assertEqual([(g[1], g[2]) for g in gates], [("a", "0"), ("b", "-1")])

    def test_parse_keeps_cmd_with_equals_and_spaces(self):
        with self.env.load_factory() as mod:
            _, gates, errors = mod.parse_gates("## Gates  \n- t exit=0 cmd=FOO=1 python3 -m x --k=v  a b\n")
        self.assertEqual(errors, [])
        self.assertEqual(gates[0][3], "FOO=1 python3 -m x --k=v  a b")


if __name__ == "__main__":
    unittest.main()
