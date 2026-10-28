"""T-0457 (G-014): `factory res` prints the HQ cap gate() computes, not a second formula."""
import re
import unittest

from test_lead_caps import CapBase


class ResHqCapTests(CapBase):
    def hq_line(self):
        return next(l for l in self.ok("res").stdout.splitlines() if l.startswith("workers live "))

    def test_res_and_gate_agree_on_small_box(self):
        self.ok("config", "max_live_agents", "6")
        self.ok("config", "reserved_worker_slots", "10")
        gate_cap = self.gate(role="lead")[2]["hq_cap"]
        self.assertEqual(gate_cap, 3)
        m = re.search(r"· hq (\d+)/(\d+) \(reserved for workers 10\)", self.hq_line())
        self.assertIsNotNone(m, self.hq_line())
        self.assertEqual(int(m.group(2)), gate_cap)

    def test_res_and_gate_agree_on_big_box(self):
        # max_live_agents 18 - reserved 10 = 8, above the clamp floor
        gate_cap = self.gate(role="lead")[2]["hq_cap"]
        self.assertEqual(gate_cap, 8)
        self.assertRegex(self.hq_line(), rf"· hq 0/{gate_cap} \(reserved for workers 10\)")


if __name__ == "__main__":
    unittest.main()
