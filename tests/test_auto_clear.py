"""T-0268: the tick clears blocking dialogs itself: a Settings Warning on the next tick (any agent, HQ included),
a permission dialog by Esc after blocked_alert_s (worker) / hq_unblock_s (HQ), unknown dialogs get no keys.
Pane texts are fixtures; keys go to the fake herdr stub only."""
import datetime as dt
import json
import os
import textwrap
import time
import unittest

from test_unblock import UnblockBase

WARN = ("Settings Warning\n\n /home/u/.claude/settings.json\n  └ permissions.deny: Invalid value\n\n"
        "The values listed above were skipped; the rest of the file is in effect.\n\n"
        "❯ 1. Continue\n  2. Exit and fix manually\n")
PERM = ("Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again\n  3. No\n")
UNKNOWN = "Choose a theme:\n❯ 1. Dark\n  2. Light\n"
NOW = time.time()


def wrap(text, width):
    return "\n".join(textwrap.fill(l, width, break_long_words=True) for l in text.split("\n"))


class AutoClear(UnblockBase):
    INPROC = True

    def setUp(self):
        super().setUp()
        self.texts = {}
        # The tick's cooldown and per-day cap read the real clock and the local date: pin the CLI to a fixed-offset zone
        # where it is about noon, so "today 00:00" is always hours ago and the date cannot roll over mid-test.
        offset = (12 - dt.datetime.now(dt.timezone.utc).hour + 11) % 24 - 11  # -11..+12: the Etc/GMT zones that exist
        tz = f"Etc/GMT{-offset:+d}" if offset else "Etc/GMT"  # POSIX sign: Etc/GMT-3 is UTC+3
        old = os.environ.get("TZ")
        os.environ["TZ"] = tz
        self.addCleanup(lambda: os.environ.__setitem__("TZ", old) if old is not None else os.environ.pop("TZ", None))
        self.local = dt.timezone(dt.timedelta(hours=offset))

    def pane(self, name, text, status="blocked"):
        """Give `name` its own pane text; earlier panes keep theirs (the stub reads one dict for every agent)."""
        self.herdr_status(**{name: status})
        self.texts[name] = text
        self.env.set_herdr(read_text=dict(self.texts))

    def keys(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]]

    def assert_read(self, *names):
        """Every named pane was really read by the tick (so a no-keys assertion is not vacuous)."""
        read = {c["argv"][2] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "read"]}
        self.assertLessEqual(set(names), read)

    def only_esc(self, name):
        self.assertEqual(self.keys(), [["agent", "send-keys", name, "esc"]])

    def test_settings_warning_wide_and_narrow_for_lead_and_worker(self):
        for name, role_pane in (("zz-lead", WARN), ("zz-coder", wrap(WARN, 24)), ("zz-boss", wrap(WARN, 11))):
            with self.subTest(name=name):
                self.pane(name, role_pane)
                self.tick()
                self.assertIn(["agent", "send-keys", name, "esc"], self.keys())
                ev = self.env.sql("SELECT detail FROM events WHERE action='settings-warning-dismissed' AND entity_id=?",
                                  (name,))
                self.assertEqual(len(ev), 1)
                self.assertIn("permissions.deny", ev[0]["detail"].replace(" ", ""))

    def test_one_director_alert_per_distinct_warning_per_day(self):
        self.pane("zz-lead", WARN)
        self.pane("zz-coder", wrap(WARN, 20))
        self.tick()
        self.tick()
        al = self.env.sql("SELECT recipient, sender, body FROM messages WHERE kind='alert' AND subject LIKE 'Settings Warning%'")
        self.assertEqual(len(al), 1)
        self.assertEqual((al[0]["recipient"], al[0]["sender"]), ("zz-director", "tick"))
        self.assertIn("settings.json", al[0]["body"])
        # a different warning is a new alert
        other = WARN.replace("permissions.deny", "env.BAD")
        self.pane("zz-lead-b", other)
        self.tick()
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE kind='alert' AND subject LIKE 'Settings Warning%'")), 2)

    def test_warning_without_exact_tail_is_unknown(self):
        self.pane("zz-lead", WARN.replace("rest of the file", "rest of a file"))
        self.tick()
        self.assertEqual(self.keys(), [])

    def test_worker_permission_dialog_waits_then_esc(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder", since=time.time() - 300)
        self.tick()
        self.assertEqual(self.keys(), [])
        self.block("zz-coder")  # an hour
        self.tick()
        self.only_esc("zz-coder")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='unblock' AND entity_id='zz-coder'")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE recipient='zz-coder' AND subject='UNBLOCK'")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE recipient='zz-lead' "
                                          "AND subject='zz-coder permission dialog auto-denied'")), 1)
        self.tick()  # re-armed: no second Esc until another timeout
        self.assertEqual(len(self.keys()), 1)

    def test_hq_permission_dialog_waits_15_min_then_esc_and_messages(self):
        self.pane("zz-lead", PERM)
        self.block("zz-lead", since=time.time() - 700)  # past 10 min, under 15
        self.tick()
        self.assertEqual(self.keys(), [])
        self.block("zz-lead", since=time.time() - 1000)
        self.tick()
        self.only_esc("zz-lead")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE recipient='zz-lead' AND subject='UNBLOCK (auto)'")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE recipient='zz-director' "
                                          "AND subject='zz-lead permission dialog auto-denied'")), 1)

    def test_never_digits_or_enter_only_esc(self):
        for name, text in (("zz-coder", PERM), ("zz-lead", PERM), ("zz-boss", WARN)):
            self.pane(name, text)
            self.block(name)
        self.tick()
        for k in self.keys():
            self.assertEqual(k[3:], ["esc"])
            self.assertNotIn(k[3], ("1", "2", "3", "enter", "Enter"))
        self.assertEqual(sorted(k[2] for k in self.keys()), ["zz-boss", "zz-coder", "zz-lead"])

    def test_unknown_dialog_gets_no_keys(self):
        for name in ("zz-coder", "zz-lead"):
            self.pane(name, UNKNOWN)
            self.block(name)
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])
        # the existing >10 min alert still fires for the worker
        self.assertEqual(len(self.alerts("zz-coder blocked >10 min")), 1)

    # acc 7 (M4494): keys only for a pane herdr reports blocked NOW with the dialog in the visible bottom region
    def scrolled(self, text):
        return text + "\n".join(f"$ output line {i}" for i in range(60)) + "\n❯ \n"

    def test_warning_only_in_scrollback_working_agent_no_keys(self):
        self.pane("zz-lead", self.scrolled(WARN), status="working")
        self.tick()
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='settings-warning-dismissed'"), [])

    def test_warning_only_in_scrollback_blocked_on_other_dialog_no_keys(self):
        self.pane("zz-lead", self.scrolled(WARN) + UNKNOWN)
        self.pane("zz-coder", self.scrolled(PERM) + UNKNOWN)
        self.block("zz-coder")
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])

    def test_dialog_followed_by_more_output_is_not_the_bottom_block_no_keys(self):
        for name, dialog in (("zz-lead", WARN), ("zz-coder", PERM)):
            self.pane(name, dialog + "$ ls\nfile.txt\n❯ \n")
            self.block(name)
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])

    def test_visible_warning_but_agent_not_blocked_no_keys(self):
        for st in ("idle", "working", "done"):
            self.pane("zz-lead", WARN, status=st)
            self.tick()
        self.assertEqual(self.keys(), [])

    def test_boss_idle_with_visible_warning_no_keys(self):
        self.pane("zz-boss", WARN, status="idle")
        self.tick()
        self.assertEqual(self.keys(), [])

    def test_boss_blocked_with_unsent_input_no_keys(self):
        self.pane("zz-boss", "❯ half typed message to the owner\n" + WARN)
        self.tick()
        self.assertEqual(self.keys(), [])
        self.pane("zz-boss", WARN + "│ > draft │\n")
        self.tick()
        self.assertEqual(self.keys(), [])
        self.pane("zz-boss", WARN)  # same dialog, empty prompt: cleared
        self.tick()
        self.only_esc("zz-boss")

    def age_dismissals(self):
        """Move every dismissal to 00:00 today in the pinned zone (hours before the pinned noon, same date)."""
        midnight = dt.datetime.now(self.local).replace(hour=0, minute=0, second=0, microsecond=0)
        self.env.sql("UPDATE events SET ts=? WHERE action='settings-warning-dismissed'",
                     (midnight.isoformat(timespec="seconds"),))

    def test_persisting_warning_esc_bounded_then_one_stuck_alert(self):
        self.pane("zz-lead", WARN)
        self.tick()
        self.tick()  # within the cooldown: no second Esc
        self.assertEqual(len(self.keys()), 1)
        for _ in range(2):  # age the previous dismissals past the cooldown
            self.age_dismissals()
            self.tick()
        self.assertEqual(len(self.keys()), 3)
        self.age_dismissals()
        self.tick()
        self.tick()
        self.assertEqual(len(self.keys()), 3)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning stuck%'")), 1)

    def test_worker_auto_deny_sends_no_second_blocked_alert(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder")
        self.tick()
        self.assertEqual(self.alerts("zz-coder blocked >10 min"), [])
        self.assertEqual(len(self.alerts("zz-coder permission dialog auto-denied")), 1)

    # round 3 B1: quoted warning text in the bottom region must not win over the real dialog (reviewer probes p1-p4)
    QUOTED = ("$ cat docs/auto-clear-dialogs.md\nSettings Warning\n /home/u/.claude/settings.json\n"
              "The values listed above were skipped; the rest of the file is in effect.\n")

    def test_p1_quoted_warning_then_unrelated_dialog_no_keys_no_alert(self):
        for name in ("zz-coder", "zz-lead"):
            self.pane(name, self.QUOTED + UNKNOWN)
            self.block(name)
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning%'"), [])

    def test_p1b_quoted_full_warning_dialog_then_unrelated_dialog_no_keys(self):
        self.pane("zz-lead", "$ cat docs\n" + WARN + UNKNOWN)
        self.tick()
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning%'"), [])

    def test_p2_permission_dialog_whose_command_quotes_the_warning_no_keys(self):
        quoted_perm = PERM.replace("cat /etc/hosts", "grep -r 'Settings Warning' ; echo 'The values listed above were "
                                   "skipped; the rest of the file is in effect.'")
        for name in ("zz-coder", "zz-lead"):
            self.pane(name, quoted_perm)
            self.block(name)  # an hour: past every timeout
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning%'"), [])

    def test_p3_boss_pane_with_quoted_warning_and_unrelated_dialog_no_keys(self):
        self.pane("zz-boss", self.QUOTED + UNKNOWN)
        self.tick()
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning%'"), [])

    def test_p4_unknown_dialog_after_quoted_warning_no_keys_no_alert(self):
        self.pane("zz-director", self.QUOTED + "\n" + UNKNOWN + "Enter to confirm · Esc to cancel\n")
        self.tick()
        self.assertEqual(self.keys(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'Settings Warning%'"), [])

    def test_quoted_warning_text_twice_or_with_permission_is_unknown(self):
        self.pane("zz-lead", WARN + WARN)
        self.pane("zz-coder", WARN + PERM)
        self.block("zz-coder")
        self.tick()
        self.assert_read("zz-coder", "zz-lead")
        self.assertEqual(self.keys(), [])

    def test_real_dialog_with_footer_is_still_cleared(self):
        self.pane("zz-lead", WARN + "Enter to confirm · Esc to cancel\n")
        self.tick()
        self.only_esc("zz-lead")

    # round 3 B2: only the classified dialog's own option lines are exempt from the unsent-input check
    def test_boss_draft_that_looks_like_an_option_is_unsent_input(self):
        for draft in ("> 2. merge it\n", "│ > 2. merge it │\n", "❯ 1. ship\n"):
            with self.subTest(draft=draft):
                self.pane("zz-boss", draft + WARN)
                self.tick()
                self.assertEqual(self.keys(), [])

    def test_boss_draft_with_empty_first_line_is_unsent_input(self):
        self.pane("zz-boss", "❯\nsecond line of my draft\n" + WARN)
        self.tick()
        self.assertEqual(self.keys(), [])
        self.pane("zz-boss", "> \n  continued text\n" + PERM)
        self.block("zz-boss")
        self.tick()
        self.assertEqual(self.keys(), [])

    def test_boss_dialog_own_options_are_not_unsent_input(self):
        self.pane("zz-boss", WARN)
        self.tick()
        self.only_esc("zz-boss")

    # T-0287: the tick never waits for a worker to settle; the nudge is finished on a later tick
    def calls_of(self, *head):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:len(head)] == list(head)]

    def nudges(self):
        return [p for p in self.calls_of("agent", "prompt") if "UNBLOCK from tick" in p[3]]

    def test_many_auto_denied_workers_one_tick_no_wait_and_nudges_follow_later(self):
        self.env.seed_agent("zz-coder-c", "coder", goal="G-001", team="g-001-a", pane_id="p-cc")
        names = ("zz-coder", "zz-coder-b", "zz-coder-c")
        for n in names:
            self.pane(n, PERM)
            self.block(n)
        st = json.loads(self.env.herdr_state.read_text())
        st["panes"].append({"pane_id": "p-cc", "tab_id": "t1", "workspace_id": "w1"})
        st["agents"].append({"name": "zz-coder-c", "agent": "claude", "pane_id": "p-cc", "agent_status": "blocked"})
        self.env.set_herdr(panes=st["panes"], agents=st["agents"])
        self.env.set_herdr(wait_sleep=60)  # a real wait would hold the tick for minutes
        t0 = time.monotonic()
        self.tick()
        self.assertLess(time.monotonic() - t0, 30)
        self.assertEqual(sorted(k[2] for k in self.keys()), sorted(names))
        self.assertEqual(self.calls_of("agent", "wait"), [])
        self.assertEqual(self.nudges(), [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='nudge-pending'")), 3)
        # the next tick, agents idle again: the nudges go out once each
        agents = json.loads(self.env.herdr_state.read_text())["agents"]
        for a in agents:
            a["agent_status"] = "idle"
        self.env.set_herdr(agents=agents)
        self.tick()
        self.tick()
        self.assertEqual(sorted(p[2] for p in self.nudges()), sorted(names))
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='nudge-done'")), 3)

    def test_nudge_waits_while_agent_still_blocked_and_is_dropped_when_not_live(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder")
        self.tick()
        self.pane("zz-coder", UNKNOWN)  # still blocked, on another dialog: never type into it
        self.tick()
        self.assertEqual(self.nudges(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='nudge-done'"), [])
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-coder'")
        self.tick()
        self.assertEqual(self.nudges(), [])
        done = self.env.sql("SELECT detail FROM events WHERE action='nudge-done'")
        self.assertEqual([json.loads(d["detail"])["outcome"] for d in done], ["dropped"])

    def idle_all(self):
        agents = json.loads(self.env.herdr_state.read_text())["agents"]
        for a in agents:
            a["agent_status"] = "idle"
        self.env.set_herdr(agents=agents)

    def test_nudge_dropped_for_same_name_relaunch_new_pane(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder")
        self.tick()
        pend = json.loads(self.env.sql("SELECT detail FROM events WHERE action='nudge-pending'")[0]["detail"])
        self.assertTrue(pend["pane_id"])
        self.assertTrue(pend["started"])
        self.env.sql("UPDATE agents SET pane_id='p-new', started='2099-01-01T00:00:00+00:00' WHERE name='zz-coder'")
        self.idle_all()
        self.tick()
        self.assertEqual(self.nudges(), [])
        done = self.env.sql("SELECT detail FROM events WHERE action='nudge-done'")
        self.assertEqual([json.loads(d["detail"])["outcome"] for d in done], ["dropped"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='nudge-mismatch'")), 1)

    def test_nudge_dropped_for_same_pane_new_start(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder")
        self.tick()
        self.env.sql("UPDATE agents SET started='2099-01-01T00:00:00+00:00' WHERE name='zz-coder'")
        self.idle_all()
        self.tick()
        self.assertEqual(self.nudges(), [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='nudge-mismatch'")), 1)

    def test_nudge_sent_when_same_pane_and_start_and_idle(self):
        self.pane("zz-coder", PERM)
        self.block("zz-coder")
        self.tick()
        self.idle_all()
        self.tick()
        self.assertEqual([p[2] for p in self.nudges()], ["zz-coder"])
        done = self.env.sql("SELECT detail FROM events WHERE action='nudge-done'")
        self.assertEqual([json.loads(d["detail"])["outcome"] for d in done], ["sent"])


if __name__ == "__main__":
    unittest.main()
