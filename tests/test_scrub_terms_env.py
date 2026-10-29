"""T-0600: factory spawn exports SCRUB_TERMS_FILE (a path) to coder/reviewer/qa/judge; `factory config <key>` prints one key."""
import json
import unittest

from harness import FactoryTestCase

TERMS = "/zz-test/terms.txt"
WITH_TERMS = ("coder", "reviewer", "qa", "judge")
WITHOUT_TERMS = ("pm", "lead", "boss", "director")


class ScrubTermsEnvTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="in_progress", team="g-001-a")

    def spawn_cmd(self, role):
        a = ["spawn", role, "--name", f"zz-{role}", "--goal", "G-001", "--team", "g-001-a"]
        if role != "lead":
            a += ["--cwd", str(self.env.home_dir)]
        return a

    def start_envs(self):
        """KEY=VALUE pairs of every --env the multiplexer got for this spawn."""
        out = []
        for c in self.env.calls("herdr"):
            argv = c["argv"]
            out += [argv[i + 1] for i, x in enumerate(argv) if x == "--env"]
        return out

    def spawn_one(self, role, queued=False):
        if queued:
            self.env.set_herdr(fail=True)
            self.assertIn("QUEUED", self.ok(*self.spawn_cmd(role)).stdout)
            self.env.set_herdr(fail=False)
            self.ok("tick")
        else:
            p = self.ok(*self.spawn_cmd(role))
            self.assertNotIn(TERMS, p.stdout + p.stderr)
        return self.start_envs()

    def test_present_for_the_four_worker_roles_immediate_and_queued(self):
        self.set_cfg(TERMS)
        for queued in (False, True):
            for role in WITH_TERMS:
                with self.subTest(role=role, queued=queued):
                    self.reset(role)
                    self.assertIn(f"SCRUB_TERMS_FILE={TERMS}", self.spawn_one(role, queued))

    def test_absent_when_config_empty(self):
        for role in WITH_TERMS:
            with self.subTest(role=role):
                self.reset(role)
                self.assertFalse([e for e in self.spawn_one(role) if e.startswith("SCRUB_TERMS_FILE")])

    def test_absent_for_pm_lead_boss_director(self):
        self.set_cfg(TERMS)
        for role in WITHOUT_TERMS:
            with self.subTest(role=role):
                self.reset(role)
                envs = self.spawn_one(role)
                self.assertIn(f"FACTORY_ROLE={role}", envs)
                self.assertFalse([e for e in envs if e.startswith("SCRUB_TERMS_FILE")])

    def test_path_never_reaches_output_events_notes_or_the_prompt(self):
        self.set_cfg(TERMS)
        p = self.ok(*self.spawn_cmd("coder"))
        self.assertNotIn(TERMS, p.stdout + p.stderr)
        # the owner's own `config` set event is the only row that may name the path
        queries = (("events", "SELECT detail AS x FROM events WHERE entity != 'config'"),
                   ("messages", "SELECT subject||body AS x FROM messages"),
                   ("agents", "SELECT spawn_args||coalesce(summary,'') AS x FROM agents"))
        for table, q in queries:
            self.assertEqual([r["x"] for r in self.env.sql(q) if r["x"] and TERMS in r["x"]], [], table)
        prompt = (self.env.home_dir / "run" / "zz-coder.prompt.md").read_text()
        self.assertNotIn(TERMS, prompt)

    def test_config_key_prints_only_that_value(self):
        p = self.ok("config", "max_live_agents")
        self.assertEqual(len(p.stdout.strip().splitlines()), 1)
        self.assertNotIn("=", p.stdout)
        full = self.ok("config").stdout
        self.assertGreater(len(full.splitlines()), 5)
        self.assertIn(f"max_live_agents = {p.stdout.strip()}", full)

    def test_config_unknown_key_is_refused(self):
        p = self.env.run("config", "no_such_key")
        self.assertEqual(p.returncode, 2)
        self.assertIn("unknown key", p.stderr)

    # -- helpers
    def set_cfg(self, value):
        self.ok("config", "scrub_terms_file", value)

    def reset(self, role):
        """Forget earlier spawns of this role so each sub-case starts clean."""
        self.env.sql("DELETE FROM agents WHERE name=?", (f"zz-{role}",))
        self.env.calls_file.write_text("")


if __name__ == "__main__":
    unittest.main()
