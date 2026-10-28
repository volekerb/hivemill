import unittest

from harness import FIXTURES, FactoryTestCase


class PaneAtShellTests(FactoryTestCase):
    def at_shell(self, **pane):
        self.env.set_herdr(panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", **pane}])
        with self.env.load_factory() as mod:
            return mod.pane_at_shell("p1")

    def test_pane_at_shell_real_shell_fixture(self):
        self.assertIs(self.at_shell(fixture=str(FIXTURES / "herdr_process_info_shell.json")), True)

    def test_pane_at_shell_real_claude_fixture(self):
        self.assertIs(self.at_shell(fixture=str(FIXTURES / "herdr_process_info_claude.json")), False)

    def test_pane_at_shell_unknown_shape_false(self):
        self.assertIs(self.at_shell(raw_result={"something": "else"}), False)
        self.assertIs(self.at_shell(raw_result={"process_info": {"foreground_processes": "zsh"}}), False)
        self.assertIs(self.at_shell(raw_result={"process_info": {}}), False)

    def test_pane_at_shell_legacy_key_fallback(self):
        self.assertIs(self.at_shell(raw_result={"foreground": [{"name": "-zsh"}]}), True)

    def test_pane_at_shell_stub_default_and_fg(self):
        self.assertIs(self.at_shell(), True)
        self.assertIs(self.at_shell(fg="claude"), False)


if __name__ == "__main__":
    unittest.main()
