"""ci/demo_seed.py through its real CLI: the live-home guard (both outcomes), content counts, determinism, scrub gate,
no writes outside the target, and every web page served from the seeded home (real `factory serve`, temp HOME)."""
import contextlib
import datetime as dt
import importlib.util
import io
import json
import os
import pwd
import re
import runpy
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
import urllib.request
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)

sys.path.insert(0, str(Path(__file__).resolve().parent))
import envcheck  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
SEED = ROOT / "ci" / "demo_seed.py"
FACTORY = ROOT / "bin" / "factory"
FACTORY_SRC = ROOT / "bin" / "factory_cli.py"
NOW = "2026-10-25T12:00:00+00:00"
PAGES = ["/", "/goals", "/goal/G-002", "/tasks", "/task/T-0004", "/agents", "/throughput", "/quality", "/resources",
         "/tokens", "/changelog"]
PREFIXES = ("FACTORY_", "HERDR_", "SCRUB_")


def default_hostname():
    """The DEFAULT_CONFIG serve_hostnames value, read from bin/factory (never a literal here)."""
    m = re.search(r'"serve_hostnames":\s*"([^"]*)"', FACTORY_SRC.read_text(encoding="utf-8"))
    assert m, "serve_hostnames default not found in bin/factory"
    return m.group(1)


def load_seed_module():
    spec = importlib.util.spec_from_file_location("demo_seed_under_test", SEED)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def scan(path, terms):
    return subprocess.run([sys.executable, str(ROOT / "ci" / "scrub_scan.py"), "--path", str(path), "--terms-file", str(terms)],
                          capture_output=True, text=True)


def clean_env(home, **extra):
    env = {k: v for k, v in os.environ.items() if not k.startswith(PREFIXES) and k != "CLAUDECODE"}
    env["HOME"] = str(home)
    env.update(extra)
    return env


def seed(home, target, *args, **extra):
    return subprocess.run([sys.executable, str(SEED), "--home", str(target), *args], capture_output=True, text=True,
                          env=clean_env(home, **extra))


def dump(db):
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        return "\n".join(con.iterdump())
    finally:
        con.close()


def rows(db, sql, *params):
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        return con.execute(sql, params).fetchall()
    finally:
        con.close()


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.home = self.root / "userhome"          # the HOME the seed runs under: its ~/factory is the "live" home
        self.home.mkdir()


    def fake_account_and_checkout(self):
        """A temp passwd home and a temp git repo carrying a copy of the seed script, standing in for the checkout."""
        acct = self.root / "account"
        repo = self.root / "checkout"
        (repo / "ci").mkdir(parents=True)
        acct.mkdir()
        shutil.copy(SEED, repo / "ci" / "demo_seed.py")
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        return acct, repo

    def cli_in_process(self, repo, acct, target):
        """Run the repo copy of the seed CLI via runpy with getpwuid pointing at the temp account; (exit code, stderr)."""
        entry = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", str(acct), "/bin/sh"))
        err = io.StringIO()
        argv = ["demo_seed.py", "--home", str(target), "--now", NOW]
        with unittest.mock.patch.object(pwd, "getpwuid", return_value=entry), unittest.mock.patch.object(sys, "argv", argv), \
                unittest.mock.patch.dict(os.environ, {"HOME": str(self.home)}), contextlib.redirect_stderr(err):
            os.environ.pop("FACTORY_HOME", None)
            try:
                runpy.run_path(str(repo / "ci" / "demo_seed.py"), run_name="__main__")
            except SystemExit as e:
                return e.code, err.getvalue()
        self.fail("seed CLI did not exit")


class Guard(Base):
    def test_live_home_refused_exit_3(self):
        for target in (self.home / "factory", self.home / "factory" / "sub", self.home):
            r = seed(self.home, target, "--now", NOW, FACTORY_HOME=str(self.home / "factory"))
            self.assertEqual(r.returncode, 3, (target, r.stderr))
            self.assertIn("refusing to seed the live factory home", r.stderr)
        self.assertEqual(list(self.home.iterdir()), [], "a refused seed must create nothing")
        # the same CLI, in-process, against a TEMP account home and a temp checkout: never a real live path
        acct, repo = self.fake_account_and_checkout()
        for target in (acct / "factory", acct / "factory" / "sub", acct / "factory" / "exports", acct / "factory" / "worktrees" / "other"):
            code, err = self.cli_in_process(repo, acct, target)
            self.assertEqual(code, 3, (target, err))
            self.assertIn("refusing to seed the live factory home", err)
        self.assertFalse((acct / "factory").exists(), "a refused seed must create nothing")

    def test_this_checkout_refused_exit_3(self):
        acct, repo = self.fake_account_and_checkout()
        for target in (repo, repo / "sub" / "demo"):
            code, err = self.cli_in_process(repo, acct, target)
            self.assertEqual(code, 3, (target, err))
            self.assertIn("refusing to seed the live factory home", err)
        self.assertFalse((repo / "sub").exists())

    @envcheck.skip_without_git()
    def test_live_home_refused_under_a_temp_home(self):
        """$HOME is a temp dir (the QA setup): the passwd home, main checkout and $FACTORY_HOME stay protected."""
        mod = load_seed_module()
        live = Path(pwd.getpwuid(os.getuid()).pw_dir) / "factory"
        main = mod.main_checkout()
        self.assertIsNotNone(main, "git common dir not found")
        with unittest.mock.patch.dict(os.environ, {"HOME": str(self.home)}):
            os.environ.pop("FACTORY_HOME", None)
            for base in (live, main):
                for sub in ("sub", "exports", "worktrees/other"):
                    with self.assertRaises(mod.Refused, msg=str(base / sub)) as cm:
                        mod.check_target(base / sub, False)
                    self.assertIn("refusing to seed the live factory home", str(cm.exception))
            mod.check_target(self.root / "fresh" / "demo", False)   # an unrelated target is still allowed

    def test_passwd_home_protected_when_getpwuid_is_patched(self):
        mod = load_seed_module()
        fake = self.root / "account"
        entry = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", str(fake), "/bin/sh"))
        with unittest.mock.patch.object(mod.pwd, "getpwuid", return_value=entry):
            with self.assertRaises(mod.Refused):
                mod.check_target(fake / "factory" / "sub", False)
            mod.check_target(fake / "elsewhere", False)

    def test_factory_home_env_refused_exit_3(self):
        fh = self.root / "fh"
        for target in (fh, fh / "sub", self.root):
            r = seed(self.home, target, "--now", NOW, FACTORY_HOME=str(fh))
            self.assertEqual(r.returncode, 3, (target, r.stderr))
            self.assertIn("refusing to seed the live factory home", r.stderr)
        self.assertFalse(fh.exists(), "a refused seed must create nothing")
        r = seed(self.home, self.root / "other", "--now", NOW, FACTORY_HOME=str(fh))
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_symlink_to_live_home_refused(self):
        (self.home / "factory").mkdir()
        link = self.root / "link"
        link.symlink_to(self.home / "factory")
        r = seed(self.home, link, "--now", NOW, FACTORY_HOME=str(self.home / "factory"))
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertFalse((self.home / "factory" / "db" / "factory.db").exists())

    def test_fresh_dir_allowed(self):
        r = seed(self.home, self.root / "d" / "demo", "--now", NOW)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue((self.root / "d" / "demo" / "db" / "factory.db").is_file())

    def test_existing_db_refused_without_force(self):
        target = self.root / "demo"
        self.assertEqual(seed(self.home, target, "--now", NOW).returncode, 0)
        before = dump(target / "db" / "factory.db")
        r = seed(self.home, target, "--now", NOW)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("--force", r.stderr)
        self.assertEqual(dump(target / "db" / "factory.db"), before)
        r = seed(self.home, target, "--now", NOW, "--force")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(dump(target / "db" / "factory.db"), before)

    def test_legacy_db_also_counts_as_existing(self):
        target = self.root / "legacy"
        target.mkdir()
        sqlite3.connect(target / "factory.db").close()
        r = seed(self.home, target, "--now", NOW)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("--force", r.stderr)

    def test_wrong_db_path_errors_instead_of_creating_a_file(self):
        mod = load_seed_module()
        missing = self.root / "factory.db"
        self.assertTrue(missing.parent.is_dir())
        with self.assertRaises(sqlite3.Error):
            mod.fill_db(missing, mod.parse_now(NOW))
        self.assertFalse(missing.exists())

    def test_seed_fills_the_db_that_init_created(self):
        target = self.root / "d2"
        self.assertEqual(seed(self.home, target, "--now", NOW).returncode, 0)
        self.assertFalse((target / "factory.db").exists())
        self.assertGreater(rows(target / "db" / "factory.db", "SELECT COUNT(*) FROM tasks")[0][0], 0)

    def test_bad_now_is_a_usage_error(self):
        r = seed(self.home, self.root / "demo", "--now", "yesterday")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertFalse((self.root / "demo").exists())

    def test_help_documents_default_now_and_home_prefix(self):
        r = subprocess.run([sys.executable, str(SEED), "--help"], capture_output=True, text=True, env=clean_env(self.home))
        flat = " ".join(r.stdout.split())
        self.assertIn("floored to the hour", flat)
        self.assertIn("HOME=<DIR>/home", flat)

    def test_no_write_outside_target(self):
        before = sorted(p.name for p in self.root.iterdir())
        r = seed(self.home, self.root / "demo", "--now", NOW)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(sorted(p.name for p in self.root.iterdir()), sorted([*before, "demo"]))
        self.assertEqual(list(self.home.iterdir()), [], "the seed must not touch the HOME it runs under")
        self.assertFalse((self.root / "demo" / ".scrub-dump.sql").exists(), "the scrub dump must be removed")


class Scrub(Base):
    def test_scrub_hit_fails_the_seed(self):
        terms = self.root / "terms.txt"
        terms.write_text("acme-web\n", encoding="utf-8")
        r = seed(self.home, self.root / "demo", "--now", NOW, SCRUB_TERMS_FILE=str(terms))
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("scrub scan failed", r.stderr)

    def test_default_config_hostname_is_empty_and_demo_sets_its_own(self):
        self.assertEqual(default_hostname(), "")  # no owner name in the default; the seed still pins a fictional one
        terms = self.root / "terms.txt"
        terms.write_text("definitely-not-in-the-demo\n", encoding="utf-8")
        target = self.root / "demo"
        r = seed(self.home, target, "--now", NOW, SCRUB_TERMS_FILE=str(terms))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(rows(target / "db" / "factory.db", "SELECT value FROM config WHERE key='serve_hostnames'"), [("demo.example",)])
        dump_file = self.root / "dump.sql"
        dump_file.write_text(dump(target / "db" / "factory.db"), encoding="utf-8")
        self.assertEqual(scan(dump_file, terms).returncode, 0, scan(dump_file, terms).stdout)

    def test_clean_terms_pass(self):
        terms = self.root / "terms.txt"
        terms.write_text("definitely-not-in-the-demo\n", encoding="utf-8")
        r = seed(self.home, self.root / "demo", "--now", NOW, SCRUB_TERMS_FILE=str(terms))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("scrub scan: no hits", r.stdout)


class Seeded(unittest.TestCase):
    """Two seeds with the same --now (determinism) shared by the content checks."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.tmp.name).resolve()
        (cls.root / "h").mkdir()
        cls.a, cls.b = cls.root / "a" / "demo", cls.root / "b" / "demo"
        cls.out = [seed(cls.root / "h", d, "--now", NOW) for d in (cls.a, cls.b)]

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_seeded_ok(self):
        for r in self.out:
            self.assertEqual(r.returncode, 0, r.stderr)

    def test_deterministic_dump(self):
        self.assertEqual(dump(self.a / "db" / "factory.db"), dump(self.b / "db" / "factory.db"))

    def test_deterministic_logs(self):
        def tree(d):
            return {str(p.relative_to(d)): p.read_text() for p in (d / "home").rglob("*") if p.is_file()}
        self.assertEqual(tree(self.a), tree(self.b))
        self.assertTrue(tree(self.a))

    def test_other_now_differs(self):
        r = seed(self.root / "h", self.root / "c" / "demo", "--now", "2026-10-26T12:00:00+00:00")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotEqual(dump(self.a / "db" / "factory.db"), dump(self.root / "c" / "demo" / "db" / "factory.db"))

    def test_counts(self):
        db = self.a / "db" / "factory.db"
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM goals")[0][0], 4)
        self.assertTrue({"done", "active", "blocked"} <= {s for (s,) in rows(db, "SELECT status FROM goals")})
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM tasks")[0][0], 18)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM messages")[0][0], 25)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM agents")[0][0], 10)
        self.assertEqual({k for (k,) in rows(db, "SELECT kind FROM messages")} & {"request", "report", "escalation", "decision"},
                         {"request", "report", "escalation", "decision"})
        self.assertEqual({s for (s,) in rows(db, "SELECT status FROM agents")} & {"live", "lost", "done"}, {"live", "lost", "done"})
        self.assertEqual({r for (r,) in rows(db, "SELECT role FROM tasks")},
                         {"pm", "coder", "qa", "reviewer", "judge", "cleaner"})
        self.assertEqual({s for (s,) in rows(db, "SELECT status FROM tasks")},
                         {"todo", "in_progress", "in_review", "done", "cancelled", "blocked"})

    def test_healthy_busy_state_gate_open_and_a_fresh_tick_sample(self):
        """The Overview System card in the demo: busy but below the cap (gate open), no 'gate closed' stress text, and a
        tick utilization row at --now so the card never shows the 'no sample' line while it is fresh."""
        db = self.a / "db" / "factory.db"
        live = rows(db, "SELECT COUNT(*) FROM agents WHERE status='live'")[0][0]
        cap = int(rows(db, "SELECT value FROM config WHERE key='max_live_agents'")[0][0])
        self.assertGreaterEqual(live, 12)
        self.assertGreaterEqual(cap - live, 3, "headroom: live %d of cap %d" % (live, cap))
        self.assertLess(live / cap, 0.9)
        self.assertEqual(rows(db, "SELECT COUNT(*) FROM events WHERE detail LIKE '%gate closed%'"), [(0,)])
        sample = rows(db, "SELECT ts, live, cap FROM util_samples")
        self.assertEqual(sample, [(NOW, live, cap)])
        usage = json.loads((self.a / "home" / ".claude" / "usage-limits.json").read_text())
        self.assertGreaterEqual(usage["updated_at"], int(dt.datetime.fromisoformat(NOW).timestamp()), "usage is fresh when served hours after --now")

    def test_minimum_counts(self):
        """The same numbers the acceptance walk checks through the CLI: goals, live agents, tasks, merges in 21 days."""
        db = self.a / "db" / "factory.db"
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM goals")[0][0], 9)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM projects")[0][0], 3)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM agents WHERE status='live'")[0][0], 12)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM tasks")[0][0], 60)
        live_roles = {r for (r,) in rows(db, "SELECT role FROM agents WHERE status='live'")}
        self.assertTrue({"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge"} <= live_roles, live_roles)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(DISTINCT team_id) FROM agents WHERE status='live' AND role='lead'")[0][0], 3)
        self.assertTrue({"done", "exited", "lost", "queued", "stopped"} <= {s for (s,) in rows(db, "SELECT status FROM agents")})
        self.assertEqual({s for (s,) in rows(db, "SELECT status FROM goals")} & {"active", "blocked", "done", "proposed"},
                         {"active", "blocked", "done", "proposed"})
        lo = (dt.datetime.fromisoformat(NOW) - dt.timedelta(days=21)).isoformat()
        merged = rows(db, "SELECT COUNT(DISTINCT entity_id) FROM events WHERE entity='task' AND action='merged' AND ts >= ?", lo)[0][0]
        self.assertGreaterEqual(merged, 60)
        self.assertEqual(rows(db, "SELECT COUNT(*) FROM events WHERE ts > ?", NOW.replace("+00:00", "") + "+00:00"), [(0,)], "no event after --now")
        # a goal waits on the owner: an unread message to the owner about it
        self.assertTrue(rows(db, "SELECT 1 FROM messages WHERE recipient='owner' AND read_at IS NULL AND goal_id IN "
                                 "(SELECT id FROM goals WHERE status IN ('proposed','blocked'))"))
        # the retry story: failures that end in a merge, and a few rejections
        self.assertGreaterEqual(rows(db, "SELECT COUNT(DISTINCT task_id) FROM verdicts WHERE verdict='fail' AND task_id IN "
                                         "(SELECT task_id FROM verdicts WHERE verdict='merge')")[0][0], 8)
        self.assertGreaterEqual(rows(db, "SELECT COUNT(*) FROM verdicts WHERE verdict='reject'")[0][0], 3)

    def merges_per_day(self, db):
        """UTC calendar day -> merged tasks, for the 21 days ending at --now's date (days without a merge count 0)."""
        today = dt.datetime.fromisoformat(NOW).date()
        got = {d: n for d, n in rows(db, "SELECT substr(ts, 1, 10), COUNT(DISTINCT entity_id) FROM events "
                                         "WHERE entity='task' AND action='merged' GROUP BY 1")}
        return [(today - dt.timedelta(days=k), got.get((today - dt.timedelta(days=k)).isoformat(), 0)) for k in range(20, -1, -1)]

    def test_weekday_rhythm_trend_and_one_dip(self):
        days = self.merges_per_day(self.a / "db" / "factory.db")
        full = days[:-1]                                    # the last day is a partial one (--now is midday)
        weekday = [n for d, n in full if d.weekday() < 5]
        weekend = [n for d, n in full if d.weekday() >= 5]
        self.assertGreater(sum(weekday) / len(weekday), sum(weekend) / len(weekend), days)
        self.assertGreaterEqual(sum(weekday) / len(weekday), 2 * sum(weekend) / len(weekend), days)
        counts = [n for _, n in days]
        self.assertTrue(all(a != b for a, b in zip(counts, counts[1:])), f"two consecutive days match: {counts}")
        mean = sum(weekday) / len(weekday)
        dips = [(d, n) for d, n in full if d.weekday() < 5 and n <= 0.35 * mean]
        self.assertEqual(len(dips), 1, (dips, days))
        half = len(full) // 2                               # mild upward trend: the newer half merges more than the older
        self.assertGreater(sum(n for _, n in full[half:]), sum(n for _, n in full[:half]), days)

    def test_core_ids_and_titles_unchanged(self):
        mod = load_seed_module()
        db = self.a / "db" / "factory.db"
        got = {i: t for i, t in rows(db, "SELECT id, title FROM tasks")}
        for t in mod.TASKS:
            self.assertEqual(got.get(t[0]), t[4], t[0])
        got = {i: t for i, t in rows(db, "SELECT id, title FROM goals")}
        for g in mod.GOALS:
            self.assertEqual(got.get(g[0]), g[2], g[0])
        self.assertEqual({tid for (tid,) in rows(db, "SELECT id FROM tasks WHERE id <= 'T-0024'")}, {t[0] for t in mod.TASKS})

    def test_every_session_agent_has_a_usage_log(self):
        db = self.a / "db" / "factory.db"
        want = {n for (n,) in rows(db, "SELECT name FROM agents WHERE started IS NOT NULL AND role NOT IN "
                                       "('reviewer','judge','staff-engineer','cleaner')")}
        logs = set()
        for f in (self.a / "home" / ".claude" / "projects").rglob("*.jsonl"):
            logs.add(json.loads(f.read_text().splitlines()[0])["agentName"])
        self.assertEqual(logs, want)

    def test_same_now_same_dump_at_the_docs_time(self):
        out = [seed(self.root / "h", self.root / n / "demo", "--now", "2026-10-20T12:00:00") for n in ("p", "q")]
        for r in out:
            self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(dump(self.root / "p" / "demo" / "db" / "factory.db"), dump(self.root / "q" / "demo" / "db" / "factory.db"))

    def test_verdicts_tell_a_rejection_story(self):
        got = set(rows(self.a / "db" / "factory.db", "SELECT role, verdict FROM verdicts"))
        self.assertTrue({("reviewer", "pass"), ("reviewer", "fail"), ("qa", "pass"), ("qa", "fail"),
                         ("judge", "merge"), ("judge", "reject")} <= got, got)

    def test_one_head_sha_per_attempt(self):
        db = self.a / "db" / "factory.db"
        tasks = {t for (t,) in rows(db, "SELECT DISTINCT task_id FROM verdicts")}
        self.assertTrue(tasks)
        for tid in tasks:
            attempts, cur = [], []
            for head, role, v in rows(db, "SELECT head_sha, role, verdict FROM verdicts WHERE task_id=? ORDER BY created, id", tid):
                cur.append((head, role, v))
                if v in ("fail", "reject", "merge"):     # ends the attempt
                    attempts.append(cur)
                    cur = []
            attempts.append(cur) if cur else None
            heads = []
            for att in attempts:
                self.assertEqual(len({h for h, _, _ in att}), 1, (tid, att))
                heads.append(att[0][0])
            self.assertEqual(len(set(heads)), len(heads), (tid, "a new attempt must get a new head"))
        # the merged event carries the merge verdict's head
        for tid, head in rows(db, "SELECT task_id, head_sha FROM verdicts WHERE verdict='merge'"):
            (detail,) = rows(db, "SELECT detail FROM events WHERE entity_id=? AND action='merged'", tid)[0]
            self.assertEqual(detail, "@" + head[:8])

    def test_no_placeholder_text(self):
        db = self.a / "db" / "factory.db"
        for spec, acc, tid in rows(db, "SELECT spec, acceptance, id FROM tasks"):
            self.assertNotIn("Demo spec", spec)
            self.assertNotIn("Demo acceptance", acc)
            self.assertTrue(2 <= len(spec.splitlines()) <= 4, (tid, spec))
            items = [ln for ln in acc.splitlines() if ln.strip()]
            self.assertTrue(2 <= len(items) <= 4, (tid, acc))
            for i, ln in enumerate(items, 1):
                self.assertTrue(ln.startswith(f"{i}. "), (tid, ln))
        for (summary,) in rows(db, "SELECT summary FROM verdicts"):
            self.assertNotIn("fix the reported findings", summary)
            self.assertNotEqual(summary, "Looks correct and fits the spec.")
        for (summary,) in rows(db, "SELECT summary FROM verdicts WHERE verdict IN ('fail','reject')"):
            self.assertRegex(summary, r"\w+[./]\w+:\d+", summary)   # a file:line finding

    def test_events_span_fourteen_days(self):
        lo, hi = rows(self.a / "db" / "factory.db", "SELECT MIN(ts), MAX(ts) FROM events WHERE action='merged'")[0]
        self.assertLess(lo, "2026-10-14")
        self.assertGreater(hi, "2026-10-25")

    def test_pr_urls_are_fictional(self):
        urls = [u for (u,) in rows(self.a / "db" / "factory.db", "SELECT pr_url FROM tasks WHERE pr_url IS NOT NULL")]
        self.assertTrue(urls)
        for u in urls:
            self.assertRegex(u, r"^https://github\.com/example-org/(acme-web|ledger-api|docs-site)/pull/\d+$")

    def test_token_logs_are_fictional_and_parse(self):
        sys.path.insert(0, str(ROOT))
        from web.tokens import TokenIndex
        idx = TokenIndex(self.a / "home" / ".claude" / "projects")
        idx.refresh()
        snap = idx.snapshot()
        self.assertGreater(snap["files"], 5)
        self.assertGreater(len(snap["rows"]), 20)
        self.assertEqual(snap["errors"], 0)
        projects = {p.name for p in (self.a / "home" / ".claude" / "projects").iterdir()}
        self.assertTrue(all(p.startswith("-srv-demo-") for p in projects), projects)
        self.assertTrue((self.a / "home" / ".claude" / "usage-limits.json").is_file())


class Pages(unittest.TestCase):
    """Real `factory serve` on a demo seeded with the default --now, under HOME=<dir>/home."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.tmp.name).resolve()
        (cls.root / "h").mkdir()
        cls.demo = cls.root / "demo"
        r = seed(cls.root / "h", cls.demo)
        assert r.returncode == 0, r.stderr
        env = clean_env(cls.demo / "home", FACTORY_HOME=str(cls.demo))
        cls.proc = subprocess.Popen([sys.executable, str(FACTORY), "serve", "--bind", "127.0.0.1", "--port", "0"],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
        line = cls.proc.stdout.readline()
        m = re.search(r"http://127\.0\.0\.1:(\d+)/", line)
        assert m, f"serve did not start: {line!r} {cls.proc.poll()}"
        cls.base = m.group(0)

    @classmethod
    def tearDownClass(cls):
        cls.proc.terminate()
        cls.proc.wait(timeout=10)
        cls.proc.stdout.close()
        cls.proc.stderr.close()
        cls.tmp.cleanup()

    def get(self, path):
        with urllib.request.urlopen(self.base.rstrip("/") + path, timeout=10) as r:
            return r.status, r.read().decode("utf-8")

    def test_every_page_renders_with_data(self):
        for path in PAGES:
            status, html = self.get(path)
            self.assertEqual(status, 200, path)
            self.assertNotIn('class="empty"', html, f"{path} shows an empty-state placeholder")
            self.assertNotIn("indexing", html, path)

    def test_tokens_page_shows_only_demo_usage(self):
        _, html = self.get("/tokens")
        self.assertIn("38%", html)                       # the fake usage-limits.json, not the owner's
        self.assertIn("G-002", html)
        self.assertNotIn(str(Path.home()), html)


if __name__ == "__main__":
    unittest.main()
