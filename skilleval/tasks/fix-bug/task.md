# Fix a bug in the task dependency resolver

The working directory holds a small Python module, `taskdeps.py`, with a
unit test file `test_taskdeps.py`. It decides in which order tasks must be
worked on, given which tasks depend on which.

## Reported bug

A task that lists a dependency which does not exist in the task map (for
example `{"T-1": ["T-9"]}`) is silently accepted, and the missing id `T-9`
even shows up in the returned order as if it were a real task. The module's
docstring says this must be an error.

`python3 -m unittest` currently fails in `test_unknown_dependency_is_an_error`.

## What to do

- Fix the module so its behaviour matches the contract in the docstring at
  the top of `taskdeps.py`. Read the whole docstring: every case it
  describes has to behave as written, not only the one reported above.
- Keep the public function names and signatures unchanged.
- Use only the Python standard library.
- Do not edit `test_taskdeps.py`; you may add further test files.

## Done means

- `python3 -m unittest` passes in the working directory.
- The fix also holds for cases the existing tests do not cover; the work
  will be checked with additional tests you cannot see.
- Leave your changes in the working directory (no commit needed).
