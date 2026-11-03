"""Make `python3 -m unittest tests.<module>` work from the repo root.

Test modules import the harness as a top-level module (`import harness`), which is how `discover -s tests`
(CI) resolves it. Importing the `tests` package puts this directory on sys.path so the dotted form resolves
it the same way. One mechanism for every module: no per-file path hacks.
"""
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)
