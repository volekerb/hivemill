# CLI layout: bin/factory and bin/factory_cli.py

`bin/factory` is a stub: it puts its own (symlink-resolved) directory on `sys.path`, imports `factory_cli` and calls `main()`.
All code lives in `bin/factory_cli.py`.

Why: a script run as `__main__` is recompiled on every start (no `.pyc`), which cost 150-200 ms for the ~540 KB CLI. An imported
module is cached in `bin/__pycache__`, so `factory --help` / `whoami` start in roughly 80-100 ms instead of 180-220 ms.

Rules:
- Run it as `python3 bin/factory ...` or through the `~/.local/bin/factory` symlink; both resolve to the stub.
- Read or import the code (tests, tools, lints) from `bin/factory_cli.py`, never from the stub (`tests/harness.py`: `FACTORY_SRC`; `FACTORY` is the stub).
- A copy of the CLI (scout/nsbox trial snapshot) must carry both files.
