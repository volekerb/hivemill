import unittest

from afterlist import parse_after


class ParseAfter(unittest.TestCase):
    def test_simple_list(self):
        self.assertEqual(parse_after("T-1,T-2"), ["T-1", "T-2"])

    def test_whitespace_around_entries(self):
        self.assertEqual(parse_after(" T-1 , T-20 "), ["T-1", "T-20"])

    def test_empty_means_no_dependencies(self):
        self.assertEqual(parse_after(""), [])


if __name__ == "__main__":
    unittest.main()
