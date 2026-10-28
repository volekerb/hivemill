# Implement parsing of the `--after` task list

The working directory holds `afterlist.py` with a stub function
`parse_after(text, known=None, self_id=None)` and a small visible test file,
`test_afterlist.py`. The function parses the value of a command-line option
such as `--after T-1,T-2` (the tasks a new task must wait for).

## What to build

Implement `parse_after` exactly as described in its docstring. In short:

- ids look like `T-<digits>`, are case-insensitive, and come back in upper case;
- surrounding whitespace is ignored; empty or `None` input gives `[]`;
- duplicates are dropped, first occurrence wins, order is kept;
- an empty entry between or after commas, an entry that is not a task id,
  an id missing from `known`, or the task's own id (`self_id`) each raise
  `ValueError` with the exact message given in the docstring.

Read every rule in the docstring; the visible tests only cover the simple case.

## Constraints

- Keep the function name and signature; use only the Python standard library.
- Do not edit `test_afterlist.py`; you may add further test files.

## Done means

- `python3 -m unittest` passes in the working directory.
- Every rule in the docstring holds, including error messages. The work
  will be checked with additional tests you cannot see.
- Leave your changes in the working directory (no commit needed).
