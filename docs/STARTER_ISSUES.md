# Starter issues

Drafts. The owner files these after the public repository exists; agents never file them.

Each draft carries the labels `good first issue` plus one area label. `bin/factory` moves daily, so its locations are symbols to grep; any line number is advisory and labelled with the commit it was read on. Other files cite `path:line` checked on 2026-10-28; re-check before filing.

## Add help text to the bare subcommands

Labels: `good first issue`, `cli`

**Problem.** `factory --help` lists every subcommand, but many show no description because their parser has no `help=`. `factory res` and `factory clean` have one, so the gap is visible next to them.

**Where.** In `bin/factory`, the parsers built with `sp.add_parser("config"`, `sp.add_parser("whoami"`, `sp.add_parser("project"`, `sp.add_parser("goal"`, `sp.add_parser("task"`, `sp.add_parser("msg"`, `sp.add_parser("inbox"` and `sp.add_parser("agents"` (line ~8172 to ~8281 on 09836488, advisory only). The examples to copy are `sp.add_parser("res", help=` and `sp.add_parser("clean", help=`. Find each with `grep -nF 'sp.add_parser("<name>"' bin/factory`.

**Acceptance.**
- `factory --help` shows a one-line description for each of the eight subcommands above.
- No option, default or behaviour changes.
- `python3 -m unittest discover -s tests` passes with no fewer tests than `main`.

**Size.** Small: about 8 lines in one file.

## Add a usage example to `factory msg --help`

Labels: `good first issue`, `cli`

**Problem.** `factory msg` takes a recipient, a subject, a body and a `--kind`, but `--help` has no example. Newcomers guess the argument order and which kinds exist.

**Where.** In `bin/factory`, `sp.add_parser("msg")` (line ~8228 on 09836488, advisory only) defines the parser and the `x.add_argument("--kind", default="msg", choices=[` line right after it the choices (`msg`, `request`, `report`, `escalation`, `decision`, `alert`). Pass `epilog=` and `formatter_class=argparse.RawDescriptionHelpFormatter` to the parser.

**Acceptance.**
- `factory msg --help` ends with an `Examples:` block of at least two commands, one with `--kind request`.
- Every example runs as written against a throwaway `FACTORY_HOME`.
- A test asserts the help output contains `Examples:` (run the CLI as a subprocess).

**Size.** Small: one parser, one test.

## Add a skip-to-content link to the web UI

Labels: `good first issue`, `web`

**Problem.** Every web UI page starts with the navigation bar, so a keyboard or screen-reader user must tab through every nav link on each page before reaching the content. The page has a visible focus style but no "Skip to content" link.

**Where.** `web/html.py:146` (`page()` builds the `nav` before the content), `web/html.py:159` (the `main` element has no `id`) and `web/static/app.css:32` (the `focus-visible` rule to reuse for the link).

**Acceptance.**
- `page()` emits `<a class="skip-link" href="#main">Skip to content</a>` as the first element of `<body>`, and the `main` element has `id="main"`.
- `web/static/app.css` hides the link off-screen until it has focus, then shows it.
- A test in `tests/` renders `page()` and asserts the link and the `id="main"` are present.

**Size.** Small: about 10 lines of Python and CSS, one test.
