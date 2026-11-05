"""T-1130: log_task_judge_hold writes the judge-hold event once per hold, not once per tick."""
import unittest

from test_judge_hold_flag import Base, LEAD, T


class HoldEventOnceTests(Base):
    def test_same_reason_twice_one_event(self):
        self.rq_pass()
        self.hold("wait for owner")
        _, out1 = self.judge_autospawn_step()
        _, out2 = self.judge_autospawn_step()
        self.assertIn(f"judge-hold {T}", out1)
        self.assertIn(f"judge-hold {T}", out2)  # the print stays on every pass
        self.assertEqual([e["detail"] for e in self.events("judge-hold")], ["wait for owner"])

    def test_changed_reason_logs_again(self):
        self.rq_pass()
        self.hold("first")
        self.judge_autospawn_step()
        self.hold("second")
        self.judge_autospawn_step()
        self.judge_autospawn_step()
        self.assertEqual([e["detail"] for e in self.events("judge-hold")], ["first", "second"])

    def test_clear_then_same_reason_logs_again(self):
        self.rq_pass()
        self.hold("same")
        self.judge_autospawn_step()
        self.ok("task", "update", T, "--judge-hold-clear", agent=LEAD)
        self.hold("same")
        self.judge_autospawn_step()
        self.judge_autospawn_step()
        self.assertEqual([e["detail"] for e in self.events("judge-hold")], ["same", "same"])

    def test_task_stays_held_every_pass(self):
        self.rq_pass()
        self.hold("same")
        for _ in range(3):
            calls, _ = self.judge_autospawn_step()
            self.assertEqual(calls, [])
            with self.env.load_factory() as f:
                con = f.db()
                self.assertEqual(f.task_judge_hold_reason(con, T), "same")
                self.assertNotIn(T, [t["id"] for t in f.mech_candidates(con)])
        self.assertEqual(len(self.events("judge-hold")), 1)


if __name__ == "__main__":
    unittest.main()
