#!/usr/bin/env python3
"""Run the unittest suite and list the slowest tests (stdlib only).

Usage (repo root): python3 tests/timing_runner.py [-n 15] [-s tests]
"""
import argparse
import sys
import time
import unittest


class TimedResult(unittest.TextTestResult):
    def __init__(self, *a, **kw):
        super().__init__(*a, **kw)
        self.timings = []
        self._t0 = 0.0

    def startTest(self, test):
        self._t0 = time.monotonic()
        super().startTest(test)

    def stopTest(self, test):
        self.timings.append((time.monotonic() - self._t0, test.id()))
        super().stopTest(test)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("-n", type=int, default=15, help="how many slowest tests to list")
    ap.add_argument("-s", "--start", default="tests", help="test directory")
    args = ap.parse_args(argv)
    suite = unittest.defaultTestLoader.discover(args.start)
    runner = unittest.TextTestRunner(resultclass=TimedResult, verbosity=0)
    res = runner.run(suite)
    print(f"\nRan {res.testsRun} tests; slowest {args.n}:")
    for secs, tid in sorted(res.timings, reverse=True)[: args.n]:
        print(f"{secs:8.2f}s  {tid}")
    return 0 if res.wasSuccessful() else 1


if __name__ == "__main__":
    sys.exit(main())
