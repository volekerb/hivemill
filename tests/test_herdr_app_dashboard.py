"""install.py --herdr-app: the Hivemill dashboard for the Herdr app (docs/herdr-app-dashboard.md).

Runs the real installer entry point against a throwaway home with a minimal Herdr app folder; never touches
the real ~/.config.
"""
import importlib.util
import io
import json
import os
import shutil
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "plugins", "hivemill", "scripts", "install.py")
MOD = os.path.join(ROOT, "plugins", "hivemill", "herdr-app", "mods", "hivemill")

spec = importlib.util.spec_from_file_location("hivemill_install_dash", SCRIPT)
ins = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ins)

MAIN = {
    "id": "root", "type": "row",
    "children": [
        {"id": "sidebar", "type": "column", "children": [{"id": "agents", "type": "block", "content": {"mod": "agents"}}]},
        {"id": "work", "type": "column", "children": [
            {"id": "main", "type": "block", "content": {"tab": "active"}},
            {"id": "status", "type": "block", "size": {"px": 28}, "content": {"mod": "workspace-status"}},
        ]},
    ],
}


class Host(ins.Host):
    def __init__(self, home):
        ins.Host.__init__(self, home=home, environ={"PATH": os.defpath})
        self.calls = []

    def run(self, argv, cwd=None, timeout=None, mutate=False):
        self.calls.append(argv)
        return 0, "{}"


class HerdrAppDashboardTest(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="hivemill-dash-")
        self.addCleanup(shutil.rmtree, self.home, True)
        self.app = os.path.join(self.home, ".config", "herdr-app")
        os.makedirs(os.path.join(self.app, "layouts"))
        self.write_main(MAIN)
        self.cfg = os.path.join(self.home, ".config", "hivemill", "dashboard.json")
        self.layout = os.path.join(self.app, "layouts", "hivemill.json")

    def write_main(self, layout):
        with open(os.path.join(self.app, "layouts", "main.json"), "w") as f:
            json.dump(layout, f)

    def run_installer(self, *argv):
        out = io.StringIO()
        self.host = Host(self.home)
        rc = ins.main(list(argv), host=self.host, out=out)
        return rc, out.getvalue()

    def load(self, path):
        with open(path) as f:
            return json.load(f)

    def test_dry_run_changes_nothing(self):
        rc, out = self.run_installer("--herdr-app", "--dry-run")
        self.assertEqual(rc, 0, out)
        self.assertIn("plan only: nothing was changed", out)
        self.assertFalse(os.path.exists(os.path.join(self.app, "mods")))
        self.assertFalse(os.path.exists(self.cfg))
        self.assertFalse(os.path.exists(self.layout))
        self.assertEqual(self.host.calls, [])

    def test_install_copies_mod_writes_config_and_layout(self):
        rc, out = self.run_installer("--herdr-app", "--yes")
        self.assertEqual(rc, 0, out)
        self.assertTrue(out.rstrip().splitlines()[-1].startswith("RESULT PASS herdr-app layout=hivemill"), out)
        for f in ("main.js", "data.js", "mod.json"):
            with open(os.path.join(MOD, f)) as a, open(os.path.join(self.app, "mods", "hivemill", f)) as b:
                self.assertEqual(a.read(), b.read(), f)
        cfg = self.load(self.cfg)
        self.assertEqual(cfg["factory"]["url"], "http://127.0.0.1:8765")
        self.assertEqual(cfg["orgs"], "auto")
        self.assertEqual(cfg["projects"], [])
        lay = self.load(self.layout)
        status = lay["children"][1]["children"][1]
        self.assertEqual(status["content"], {"mod": "hivemill", "props": {"mode": "bar"}})
        # Nothing else in the layout changes, and main.json itself is left alone.
        status["content"] = {"mod": "workspace-status"}
        self.assertEqual(lay, MAIN)
        self.assertEqual(self.load(os.path.join(self.app, "layouts", "main.json")), MAIN)
        self.assertEqual(self.host.calls, [["herdr-app", "do", "layout.switch", '{"name": "hivemill"}']])

    def test_rerun_keeps_projects_and_changes_only_given_flags(self):
        os.makedirs(os.path.dirname(self.cfg))
        with open(self.cfg, "w") as f:
            json.dump({"factory": {"url": "http://127.0.0.1:9000", "web_url": "http://box:9000"}, "orgs": "auto",
                       "projects": [{"name": "App", "github": "o/app", "space": "app"}], "usage_file": "~/u.json"}, f)
        rc, out = self.run_installer("--herdr-app", "--yes", "--no-switch", "--orgs", "claude", "--factory-machine", "box")
        self.assertEqual(rc, 0, out)
        cfg = self.load(self.cfg)
        self.assertEqual(cfg["orgs"], ["claude"])
        self.assertEqual(cfg["factory"], {"url": "http://127.0.0.1:9000", "web_url": "http://box:9000", "machine": "box"})
        self.assertEqual(cfg["projects"], [{"name": "App", "github": "o/app", "space": "app"}])
        self.assertEqual(cfg["usage_file"], "~/u.json")
        self.assertEqual(self.host.calls, [])

    def test_empty_machine_clears_it(self):
        os.makedirs(os.path.dirname(self.cfg))
        with open(self.cfg, "w") as f:
            json.dump({"factory": {"url": "http://127.0.0.1:8765", "machine": "box"}}, f)
        rc, out = self.run_installer("--herdr-app", "--yes", "--no-switch", "--factory-machine", "")
        self.assertEqual(rc, 0, out)
        self.assertIsNone(self.load(self.cfg)["factory"]["machine"])

    def test_layout_without_a_status_bar_gets_one_under_the_work_area(self):
        main = json.loads(json.dumps(MAIN))
        del main["children"][1]["children"][1]
        self.write_main(main)
        rc, out = self.run_installer("--herdr-app", "--yes", "--no-switch")
        self.assertEqual(rc, 0, out)
        work = self.load(self.layout)["children"][1]["children"]
        self.assertEqual(work[-1]["content"], {"mod": "hivemill", "props": {"mode": "bar"}})
        self.assertEqual(work[-1]["size"], {"px": 28})

    def test_no_sidebar_hides_it_and_survives_a_rerun(self):
        for _ in range(2):
            rc, out = self.run_installer("--herdr-app", "--yes", "--no-switch", "--no-sidebar")
            self.assertEqual(rc, 0, out)
            self.assertIn("the sidebar hidden", out)
            lay = self.load(self.layout)
            self.assertTrue(lay["children"][0]["hidden"])
        rc, out = self.run_installer("--herdr-app", "--yes", "--no-switch")
        self.assertNotIn("hidden", self.load(self.layout)["children"][0])

    def test_no_herdr_app_is_a_hand_off_not_a_failure(self):
        os.remove(os.path.join(self.app, "layouts", "main.json"))
        rc, out = self.run_installer("--herdr-app", "--yes")
        self.assertEqual(rc, 3, out)
        self.assertIn("Install the Herdr app", out)
        self.assertFalse(os.path.exists(os.path.join(self.app, "mods")))
        self.assertFalse(os.path.exists(self.cfg))

    def test_broken_config_fails_the_config_step_without_writing(self):
        os.makedirs(os.path.dirname(self.cfg))
        with open(self.cfg, "w") as f:
            f.write("{not json")
        rc, out = self.run_installer("--herdr-app", "--yes")
        self.assertEqual(rc, 1, out)
        self.assertIn("RESULT FAIL step=herdr-app-preflight", out)
        self.assertFalse(os.path.exists(self.layout))

    def test_usage_errors(self):
        for argv in (["--herdr-app", "--check"],
                     ["--dry-run", "--orgs", "claude"],
                     ["--yes", "--factory-url", "http://x"],
                     ["--dry-run", "--no-sidebar"],
                     ["--herdr-app", "--yes", "--orgs", "openai"],
                     ["--herdr-app", "--yes", "--factory-url", "ftp://x"],
                     ["--herdr-app", "--yes", "--factory-machine", "-oProxy=x"]):
            rc, out = self.run_installer(*argv)
            self.assertEqual(rc, 2, argv)
        self.assertFalse(os.path.exists(self.cfg))

    def test_shipped_mod_files(self):
        with open(os.path.join(MOD, "mod.json")) as f:
            self.assertEqual(json.load(f)["place"], "none")
        with open(os.path.join(ROOT, "plugins", "hivemill", "herdr-app", "dashboard.example.json")) as f:
            example = json.load(f)
        self.assertEqual(set(example), {"factory", "orgs", "projects", "usage_file", "alerts", "context_pattern"})
        with open(os.path.join(MOD, "data.js")) as f:
            src = f.read()
        for key in example:
            self.assertIn("%s:" % key, src, "data.js DEFAULTS must know every example key: %s" % key)


if __name__ == "__main__":
    unittest.main()
