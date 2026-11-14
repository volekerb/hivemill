"""G-016 T-0355: ci/guard_impl_unittest.py --routed-rest lists the hook_run-routed modules the guard-diff set does not run."""
import io
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

import harness

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "ci", "guard_impl_unittest.py")
WF = os.path.join(ROOT, ".github", "workflows", "tests.yml")
# stems, .py appended at runtime (the coverage test greps test sources for the hook path spelling)
DIFF_SET = [s + ".py" for s in ("test_worker_guard", "test_priv_guard", "test_guard_diff", "test_worker_guard_public_text")]


def run(*args):
    return subprocess.run([sys.executable, TOOL, *args], capture_output=True, text=True, cwd=ROOT)


class RoutedRestSelectorTest(unittest.TestCase):
    def test_list_routed_rest_is_disjoint_from_the_diff_set(self):
        r = run("list", "--routed-rest")
        self.assertEqual(r.returncode, 0, r.stderr)
        names = r.stdout.split()
        self.assertGreater(len(names), 10)
        self.assertTrue(set(names).isdisjoint(DIFF_SET))
        self.assertIn("test_worker_guard_git.py", names)
        self.assertNotIn("test_guard_build.py", names)  # allowlisted: never runs the hook
        self.assertNotIn("test_guard_impl_coverage.py", names)

    def test_every_listed_module_exists_and_uses_the_helper(self):
        for n in run("list", "--routed-rest").stdout.split():
            path = os.path.join(ROOT, "tests", n)
            self.assertTrue(os.path.isfile(path), n)
            with open(path, encoding="utf-8") as f:
                self.assertIn("guard_impl", f.read(), n)

    def test_modules_routed_rest_gives_dotted_paths(self):
        names = run("list", "--routed-rest").stdout.split()
        mods = run("modules", "--routed-rest").stdout.split()
        self.assertEqual(mods, ["tests." + n[:-3] for n in names])

    def test_default_list_is_unchanged(self):
        self.assertEqual(run("list").stdout.split(), DIFF_SET)
        self.assertEqual(run("modules").stdout.split(), ["tests." + n[:-3] for n in DIFF_SET])

    def test_workflow_names_no_module_and_keeps_the_strict_gate_in_guard_equiv(self):
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        self.assertEqual(text.count("test_worker_guard"), 0)
        strict_job = text.split("  guard-equiv:\n", 1)[1].split("\n  guard-equiv-fanin:\n", 1)[0]
        rest_job = text.split("  guard-equiv-rest:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        self.assertIn("--impl go", strict_job)
        self.assertIn("--strict", strict_job)
        self.assertNotIn("--routed-rest", strict_job.replace("list --routed-rest", ""))
        self.assertNotIn("--strict", rest_job)
        self.assertNotIn("continue-on-error", strict_job + rest_job)
        self.assertIn("run --routed-rest", rest_job)
        self.assertEqual(text.count("--impl go"), 1)
        self.assertEqual(text.count("--strict"), 1)
        self.assertIn("needs: [ci-tier, docs-check, unittest-shard, go, guard-unittest, guard-diff, guard-self-compare, guard-evaluator-home-loosen, guard-equiv-fanin, guard-equiv-rest]", text)
        self.assertIn("needs.guard-equiv-fanin.result", text)
        self.assertIn("needs.guard-equiv-rest.result", text)
        self.assertIn("if: ${{ !cancelled() && steps.paths.outputs.run == 'true' }}", rest_job)  # REAL_GO logs after a red step

    def test_unittest_job_fails_when_either_equiv_job_is_not_success(self):
        """AC7: run the real `unittest` gate script with each result value; only success/success passes."""
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        gate = text.split("\n  unittest:\n", 1)[1].split("        run: |\n", 1)[1].split("      - uses:", 1)[0]
        script = "\n".join(l[10:] for l in gate.splitlines())
        base = {"SHARD_RESULT": "success", "GO_RESULT": "success", "GUARD_UNITTEST_RESULT": "success",
                "GUARD_DIFF_RESULT": "success", "GUARD_SELF_COMPARE_RESULT": "success",
                "GUARD_EVALUATOR_HOME_LOOSEN_RESULT": "success",
                "GUARD_EQUIV_RESULT": "success", "GUARD_EQUIV_REST_RESULT": "success"}

        def rc(**over):
            env = dict(os.environ, **base)
            env.update(over)
            return subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)

        self.assertEqual(rc().returncode, 0)
        for key in ("GUARD_EQUIV_RESULT", "GUARD_EQUIV_REST_RESULT"):
            for bad in ("failure", "skipped", "cancelled"):
                r = rc(**{key: bad})
                self.assertNotEqual(r.returncode, 0, (key, bad))
                self.assertIn(bad, r.stderr)

    def test_guard_equiv_runs_on_every_non_docs_pr(self):
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        on = text.split("\non:\n", 1)[1].split("\n\n", 1)[0]
        self.assertNotIn("paths", on)
        strict = text.split("  guard-equiv:\n", 1)[1].split("\n  guard-equiv-fanin:\n", 1)[0]
        rest = text.split("  guard-equiv-rest:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        docs_if = "needs.ci-tier.outputs.mode != 'docs'"
        for part in (strict, rest):
            header = part.split("    steps:\n", 1)[0]
            self.assertIn(docs_if, header)
            ifs = [ln for ln in header.splitlines() if ln.startswith("    if:")]
            self.assertEqual(len(ifs), 1, ifs)
        self.assertIn('echo "run=false"', strict + rest)  # non-guard paths skip the steps, the job still succeeds

    def test_guard_equiv_keeps_name_timeout_and_shape(self):
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        strict = text.split("  guard-equiv:\n", 1)[1].split("\n  guard-equiv-fanin:\n", 1)[0]
        fanin = text.split("  guard-equiv-fanin:\n", 1)[1].split("\n  guard-equiv-rest:\n", 1)[0]
        rest = text.split("  guard-equiv-rest:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        job = strict + rest
        self.assertIn("    name: guard-equiv\n", fanin)
        self.assertIn("    name: guard-equiv-rest\n", rest)
        self.assertEqual(job.count("    timeout-minutes: 30\n"), 2)  # T-1009: split the job, never a raise
        self.assertIn("--roles all --role-shard ${{ matrix.shard }} --role-shards 4", strict)
        self.assertIn("--mode ${{ matrix.shard == 1 && 'both' || 'worker' }} --strict", strict)
        self.assertIn("run --routed-rest", rest)

    def test_unknown_selector_flag_is_refused(self):
        self.assertNotEqual(run("list", "--bogus").returncode, 0)


RAW = "#raw\n"  # body is a whole module, not a TestCase body


def _mod(d, name, body):
    if body.startswith(RAW):
        with open(os.path.join(d, name), "w", encoding="utf-8") as f:
            f.write(body)
        return
    with open(os.path.join(d, name), "w", encoding="utf-8") as f:
        f.write("import unittest\nclass T(unittest.TestCase):\n" + body + "\n")


PASS = "    def test_ok(self):\n        pass\n"
FAIL = "    def test_bad(self):\n        self.fail('x')\n    def test_ok(self):\n        pass\n"


class RoutedRestRunnerTest(unittest.TestCase):
    def go(self, mods, jobs=None):
        sys.path.insert(0, os.path.join(ROOT, "ci"))
        try:
            import guard_impl_unittest as g
        finally:
            sys.path.pop(0)
        with tempfile.TemporaryDirectory() as d:
            for n, b in mods:
                _mod(d, n, b)
            out = io.StringIO()
            rc = g.run_routed_rest(out=out, names=[n for n, _ in mods], tests_dir=d, jobs=jobs)
        return rc, out.getvalue()

    def test_failing_module_does_not_stop_later_ones(self):
        rc, out = self.go([("test_a.py", FAIL), ("test_b.py", PASS)])
        self.assertEqual(rc, 1)
        self.assertIn("module test_a.py: ran=2 failures=1 errors=0 skipped=0 rc=1", out)
        self.assertIn("module test_b.py: ran=1 failures=0 errors=0 skipped=0 rc=0", out)
        self.assertIn("summary: modules_run=2 modules_failed=1 tests_run=3 tests_skipped=0 tests_failed=1 failed=test_a.py", out)

    def test_modules_run_in_parallel_and_report_in_list_order(self):
        # Each module waits (max 20s) until all three have started: only true concurrency lets them finish.
        # A serial runner would see 1 marker, time out and fail the test. Output stays in list order.
        barrier = (RAW + "import os, time, unittest\nD = os.environ['T1009_DIR']\n"
                   "class T(unittest.TestCase):\n    def test_meet(self):\n"
                   "        open(os.path.join(D, %r), 'w').close()\n"
                   "        end = time.time() + 20\n"
                   "        while len(os.listdir(D)) < 3 and time.time() < end:\n            time.sleep(0.05)\n"
                   "        self.assertEqual(len(os.listdir(D)), 3)\n")
        with tempfile.TemporaryDirectory() as sync:
            os.environ["T1009_DIR"] = sync
            self.addCleanup(os.environ.pop, "T1009_DIR", None)
            mods = [("test_%s.py" % c, barrier % c) for c in "abc"]
            rc, out = self.go(mods, jobs=3)
        self.assertEqual(rc, 0, out)
        lines = [l.split(":")[0] for l in out.splitlines() if l.startswith("module ")]
        self.assertEqual(lines, ["module test_a.py", "module test_b.py", "module test_c.py"])

    def test_parallel_red_module_output_is_whole_and_every_module_runs(self):
        rc, out = self.go([("test_a.py", PASS), ("test_b.py", FAIL), ("test_c.py", PASS), ("test_d.py", FAIL)], jobs=4)
        self.assertEqual(rc, 1)
        self.assertIn("modules_run=4 modules_failed=2 tests_run=6 tests_skipped=0 tests_failed=2 failed=test_b.py,test_d.py", out)
        # a red module's traceback sits directly before its own result line, never split by another module's lines
        for m in ("test_b.py", "test_d.py"):
            before = out.split("module %s: ran=" % m)[0].rstrip().splitlines()
            self.assertIn("FAILED (failures=1)", before[-1])
        self.assertEqual(out.count("FAIL: test_bad"), 2)

    def test_jobs_flag_is_accepted_and_bounded(self):
        sys.path.insert(0, os.path.join(ROOT, "ci"))
        try:
            import guard_impl_unittest as g
        finally:
            sys.path.pop(0)
        self.assertTrue(1 <= g.default_jobs() <= 4)
        self.assertNotEqual(run("run", "--routed-rest", "--jobs", "x").returncode, 0)

    def test_first_fails_last_passes_is_still_red(self):
        rc, _ = self.go([("test_a.py", FAIL), ("test_b.py", PASS), ("test_c.py", PASS)])
        self.assertNotEqual(rc, 0)

    def test_all_pass_is_green(self):
        rc, out = self.go([("test_a.py", PASS), ("test_b.py", PASS)])
        self.assertEqual(rc, 0)
        self.assertIn("modules_failed=0", out)
        self.assertIn("failed=-", out)

    def test_all_skipped_module_is_red_and_skip_count_is_logged(self):
        skip = "    @unittest.skip('r')\n    def test_s(self):\n        pass\n"
        mixed = skip + PASS
        rc, out = self.go([("test_a.py", skip), ("test_b.py", mixed)])
        self.assertEqual(rc, 1)
        self.assertIn("module test_a.py: ALL 1 tests skipped", out)
        self.assertIn("module test_a.py: ran=1 failures=0 errors=0 skipped=1 rc=0", out)
        self.assertIn("module test_b.py: ran=2 failures=0 errors=0 skipped=1 rc=0", out)
        self.assertIn("modules_failed=1 tests_run=3 tests_skipped=2 tests_failed=0 failed=test_a.py", out)

    def check(self, body, rc_want, **nums):
        rc, out = self.go([("test_f.py", body)])
        self.assertEqual(rc, rc_want, out)
        for k, v in nums.items():
            self.assertIn("%s=%d" % (k, v), out)
        return out

    def test_every_form_of_nothing_ran_is_red(self):
        t = "class T(unittest.TestCase):\n"
        for body in (
            "    @unittest.skip('r')\n    def test_s(self):\n        pass\n",  # f_method
            RAW + "import unittest\nraise unittest.SkipTest('r')\n",  # f_import
            RAW + "import unittest\n" + t + "    @classmethod\n    def setUpClass(cls):\n        raise unittest.SkipTest('r')\n"
            "    def test_s(self):\n        pass\n",  # f_class
            RAW + "import unittest\ndef setUpModule():\n    raise unittest.SkipTest('r')\n" + t + "    def test_s(self):\n        pass\n",  # f_module
            RAW + "import unittest\n",  # f_empty
            "    def test_s(self):\n        for i in range(2):\n            with self.subTest(i=i):\n                self.skipTest('r')\n",  # f_subtest_all
            RAW + "import os, unittest\n" + t + "    def test_s(self):\n        os._exit(0)\n",  # f_exit0
        ):
            with self.subTest(body=body):
                self.check(body, 1)

    def test_skips_beside_real_passes_are_green(self):
        t = "class %s(unittest.TestCase):\n"
        mix = (RAW + "import unittest\n" + t % "A" + "    @classmethod\n    def setUpClass(cls):\n        raise unittest.SkipTest('r')\n"
               "    def test_a(self):\n        pass\n" + t % "B" + "    def test_b(self):\n        pass\n")
        self.check(mix, 0, skipped=1)  # f_mix
        sub = ("    def test_s(self):\n        for i in range(2):\n            with self.subTest(i=i):\n"
               "                if i == 0:\n                    self.skipTest('r')\n")
        self.check(sub, 0)  # f_subtest_mix

    def test_fake_unittest_totals_on_stdout_are_not_counted(self):
        noise = "    def test_ok(self):\n        print('Ran 7 tests')\n        print('OK (skipped=7)')\n"
        out = self.check(noise, 0, tests_run=1, tests_skipped=0)  # f_stdoutnoise
        self.assertIn("skipped=0 rc=0", out)

    def test_partly_skipped_module_alone_is_green(self):
        rc, _ = self.go([("test_b.py", "    @unittest.skip('r')\n    def test_s(self):\n        pass\n" + PASS)])
        self.assertEqual(rc, 0)


class PathListTest(unittest.TestCase):
    job_index = 0  # 0 = guard-equiv, 1 = guard-equiv-rest

    def step(self):
        with open(WF, encoding="utf-8") as f:
            job = f.read().split("  guard-equiv:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        job = job.split("  guard-equiv-rest:\n")[self.job_index]
        return job.split("Guard paths changed?", 1)[1].split("- uses: actions/setup-python", 1)[0]

    def test_path_step_covers_guard_impl_and_derives_routed_modules(self):
        step = self.step()
        self.assertIn("tests/guard_impl.py", step)
        self.assertIn("list --routed-rest", step)
        self.assertIn("guard_impl_unittest.py list", step)
        self.assertIn("tests/test_guard_build.py", step)

    def script(self):
        step = self.step()
        body = step.split("run: |\n", 1)[1]
        return "\n".join(l[10:] for l in body.splitlines())

    def decide(self, changed, tool_src=None, edit=None, broken_tar=False, stdout=None, event="pull_request", before=None):
        """Execute the real "Guard paths changed?" script in a temp repo; return the run= output."""
        with tempfile.TemporaryDirectory() as d:
            def git(*a):
                return harness.git("-c", "user.name=t", "-c", "user.email=t@t", *a, cwd=d)
            git("init", "-q")
            os.makedirs(os.path.join(d, "ci"))
            shutil.copytree(os.path.join(ROOT, "tests"), os.path.join(d, "tests"),
                            ignore=shutil.ignore_patterns("__pycache__"))
            shutil.copy(TOOL, os.path.join(d, "ci", "guard_impl_unittest.py"))
            if tool_src is not None:
                # committed in the BASE commit: the tool is not in the diff, so the only
                # route to run=true is the fail-open branch (not the ci/guard_impl_unittest.py regex)
                with open(os.path.join(d, "ci", "guard_impl_unittest.py"), "w") as f:
                    f.write(tool_src)
            with open(os.path.join(d, "README.md"), "w") as f:
                f.write("x\n")
            git("add", "-A")
            git("commit", "-q", "-m", "base")
            base = harness.git("rev-parse", "HEAD", cwd=d)
            for rel, fn in (edit or {}).items():
                path = os.path.join(d, rel)
                if fn is None:
                    os.remove(path)
                else:
                    with open(path, encoding="utf-8") as f:
                        src = f.read()
                    with open(path, "w", encoding="utf-8") as f:
                        f.write(fn(src))
            for rel in changed:
                path = os.path.join(d, rel)
                os.makedirs(os.path.dirname(path), exist_ok=True)
                with open(path, "a") as f:
                    f.write("# change\n")
            git("add", "-A")
            git("commit", "-q", "-m", "pr", "--allow-empty")
            out = os.path.join(d, "gh_output")
            env = dict(os.environ, EVENT=event, BASE_SHA=base, GITHUB_OUTPUT=out, RUNNER_TEMP=d)
            if event == "push":
                env["BASE_SHA"] = "deadbeef"  # a push must diff BEFORE, not the empty pull_request base
                env["BEFORE"] = base if before is None else before
            if broken_tar:
                # only the BASE export fails: the HEAD lists are read by python, never through tar
                shim = tempfile.mkdtemp(dir=d)
                with open(os.path.join(shim, "tar"), "w") as f:
                    f.write("#!/bin/sh\nexit 1\n")
                os.chmod(os.path.join(shim, "tar"), 0o755)
                env["PATH"] = shim + os.pathsep + env["PATH"]
            r = subprocess.run(["bash", "-e", "-c", self.script()], cwd=d, env=env, capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            if stdout is not None:
                stdout.append(r.stdout)
            with open(out) as f:
                return f.read().strip().splitlines()[-1]

    def test_step_runs_for_guard_impl_only(self):
        self.assertEqual(self.decide(["tests/guard_impl.py"]), "run=true")

    def test_step_runs_for_one_routed_module_only(self):
        name = run("list", "--routed-rest").stdout.split()[0]
        self.assertEqual(self.decide(["tests/" + name]), "run=true")

    def test_step_skips_for_readme_only(self):
        self.assertEqual(self.decide(["README.md"]), "run=false")

    def test_step_skips_for_empty_diff(self):
        # an empty diff must not match a blank pattern (the empty-path bug)
        self.assertEqual(self.decide([]), "run=false")

    def test_step_fails_open_when_list_fails_or_is_empty(self):
        self.assertEqual(self.decide(["README.md"], "import sys\nsys.exit(1)\n"), "run=true")
        self.assertEqual(self.decide(["README.md"], "pass\n"), "run=true")
        # only --routed-rest fails (plain `list` still works), and only it prints nothing
        only_rest_fails = ("import sys\nif '--routed-rest' in sys.argv:\n    sys.exit(1)\nprint('test_x.py')\n")
        self.assertEqual(self.decide(["README.md"], only_rest_fails), "run=true")
        only_rest_empty = ("import sys\nif '--routed-rest' not in sys.argv:\n    print('test_x.py')\n")
        self.assertEqual(self.decide(["README.md"], only_rest_empty), "run=true")

    # T-1008: a shrink of the routed list since BASE must run the gate, in both jobs
    def routed_module(self):
        return "tests/" + run("list", "--routed-rest").stdout.split()[0]

    def test_step_runs_when_a_routed_module_is_deleted(self):
        m = self.routed_module()
        self.assertEqual(self.decide([], edit={m: None}), "run=true")

    def test_step_runs_when_a_python_only_marker_is_added(self):
        m = self.routed_module()
        self.assertEqual(self.decide([], edit={m: lambda t: t + "\nGUARD_IMPL_PYTHON_ONLY = True\n"}), "run=true")

    def test_step_runs_when_hook_run_is_removed(self):
        m = self.routed_module()
        strip = lambda t: t.replace("guard_impl", "guard_imp1")
        self.assertEqual(self.decide([], edit={m: strip}), "run=true")

    def test_step_runs_when_base_list_is_unreadable(self):
        # README-only diff, HEAD tool intact and unchanged: nothing but the failed BASE read can give run=true
        out = []
        self.assertEqual(self.decide(["README.md"], broken_tar=True, stdout=out), "run=true")
        self.assertIn("base test list unreadable or empty: running", out[0])

    def test_step_skips_for_readme_only_with_a_readable_base(self):
        self.assertEqual(self.decide(["README.md"]), "run=false")

    def test_push_skips_a_readme_and_runs_a_guard_path(self):
        self.assertEqual(self.decide(["README.md"], event="push"), "run=false")
        self.assertEqual(self.decide(["ci/guard_diff.py"], event="push"), "run=true")

    def test_push_with_no_parent_runs(self):
        self.assertEqual(self.decide(["README.md"], event="push", before=""), "run=true")
        self.assertEqual(self.decide(["README.md"], event="push", before="0" * 40), "run=true")


class PathListRestJobTest(PathListTest):
    job_index = 1


if __name__ == "__main__":
    unittest.main()
