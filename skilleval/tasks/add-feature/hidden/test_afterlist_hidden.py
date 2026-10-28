import unittest

from afterlist import parse_after


class Hidden(unittest.TestCase):
    def test_case_normalized_and_deduped_in_order(self):
        self.assertEqual(parse_after("t-2, T-1, T-2, t-1"), ["T-2", "T-1"])

    def test_whitespace_only_and_none(self):
        self.assertEqual(parse_after("   "), [])
        self.assertEqual(parse_after(None), [])

    def test_empty_entries(self):
        for text in ("T-1,,T-2", "T-1,", ",T-1", "T-1, ,T-2"):
            with self.subTest(text=text):
                with self.assertRaises(ValueError) as cm:
                    parse_after(text)
                self.assertEqual(str(cm.exception), "empty entry in --after list")

    def test_invalid_entry_reports_original_text(self):
        for bad in ("X-1", "T-", "T-1a", "T 1", "T--1"):
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError) as cm:
                    parse_after("T-1, %s " % bad)
                self.assertEqual(str(cm.exception), "invalid task id: %s" % bad)

    def test_unknown_checked_after_normalizing(self):
        known = {"T-1", "T-2"}
        self.assertEqual(parse_after("t-1", known=known), ["T-1"])
        with self.assertRaises(ValueError) as cm:
            parse_after("t-1,t-9", known=known)
        self.assertEqual(str(cm.exception), "unknown task id: T-9")

    def test_self_dependency(self):
        with self.assertRaises(ValueError) as cm:
            parse_after("T-1,t-5", self_id="T-5")
        self.assertEqual(str(cm.exception), "task cannot depend on itself: T-5")
        self.assertEqual(parse_after("T-1", self_id="T-5"), ["T-1"])


if __name__ == "__main__":
    unittest.main()
