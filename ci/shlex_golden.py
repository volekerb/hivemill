#!/usr/bin/env python3
"""Golden data for the Go port of CPython shlex (go/internal/shlex, G-016 T-0358).

Runs the real CPython shlex over (a) every command in ci/guard_corpus.json and
(b) a hand list of edge cases, in the shapes the hook uses, and writes
go/internal/shlex/testdata/golden.json. The Go test asserts identical tokens
and errors.

  python3 ci/shlex_golden.py           write the golden file
  python3 ci/shlex_golden.py --check   exit 1 if the file is stale

Not extracted: the strings the guard test modules pass to the hook. They are
built by helper calls and f-strings across ~20 modules, so no static extraction
gives exactly what the hook receives; the corpus is the shared command list.

Reference interpreter: CPython 3.13 (the version in .github/workflows/tests.yml).
Another minor version is refused: its shlex.py may differ.
"""
import argparse
import json
import os
import shlex
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORPUS = os.path.join(ROOT, "ci", "guard_corpus.json")
GOLDEN = os.path.join(ROOT, "go", "internal", "shlex", "testdata", "golden.json")
PYTHON = "3.13"

# Modes: "script" = Analyzer.script (worker-guard.py), "default" = shlex.shlex(posix, punctuation_chars=True)
# untouched, "split" = shlex.split, "quote" = shlex.quote.
LEX_MODES = ("script", "default", "split")

EDGES = [
    "",
    " ",
    "\t\n",
    "a 'b",
    'a "b',
    "a \\",
    "\\",
    "'",
    '"',
    "''",
    '""',
    "a''b",
    "x>&2",
    "a|&b",
    "a;;b",
    "a&&b",
    "a||b",
    "a;b",
    "a ; b",
    "(a)",
    "$(a b)",
    "`a b`",
    "echo $'a b'",
    "echo $'a\\'b'",
    "echo 'it'\"'\"'s'",
    "echo \"a 'b' c\"",
    "echo 'a \"b\" c'",
    "echo \"a\\\"b\"",
    "echo \"a\\nb\"",
    "echo \"a\\\\b\"",
    "echo 'a\\'b",
    "echo a\\ b",
    "echo a\\\nb",
    "cat <<EOF\nx\nEOF",
    "cat <<'EOF'\n$x\nEOF",
    "cat <<-EOF > f\n\tx\nEOF",
    "a # comment\nb",
    "a#b",
    "#only",
    "a 'b#c' d # e",
    "a;# c\nb",
    "ls>out",
    "ls >out 2>&1",
    "2>&1",
    "a&>b",
    "a|b|c",
    "a|| b",
    "a <(b) c",
    "a >(b)",
    "a;;;b",
    "a &",
    "& a",
    "a~b*c?d=e",
    "a-b.c/d",
    "FOO=bar baz",
    "NAME=$(mktemp -d)",
    "x=`y`",
    "café naïve",
    "straße ÿ Þ",
    "é>x",
    "aéb|c",
    "中文 词",
    "中文>x",
    "echo 中文;ls",
    "µ ª × ÷",
    "a b",
    "a b",
    "\U0001f600 x",
    "a\rb",
    "a\x00b",
    "a\x0bb",
    "tab\there",
    "'a'b'c'd",
    "a'b c'd",
    "\"\"a",
    "a\"\"",
    "'' ''",
    "a\\\\",
    "a\\\\\\",
    "\"a\\",
    "'a\\",
    "\"\\",
    "a\\'b",
    "a\\\"b",
    "\"a\\'b\"",
    "\"a\\$b\"",
    "'a'\\''b'",
    "a >&2 b",
    "a<&-",
    "1>&2",
    "echo a\\;b",
    "echo \"a;b\"",
    "echo 'a|b'",
    "git commit -m \"it's\"",
    "python3 -c 'print(1)'",
    "sh -c \"echo $(whoami)\"",
    "a\n\nb",
    "a\\\n",
    "# c\n# d",
    "a #",
    "a '#' b",
    "(((",
    ")))",
    "a;(b)&",
    "<<<'x'",
    "a<<<b",
    "a>>b",
    "a>|b",
    "a;&b",
    "a&;b",
    "a'b'",
    "a'b",
    "ab\\",
    "~/x",
    "*.py",
    "a=b=c",
    "--flag=val -x",
    "a:b,c@d%e+f",
    "a[0]",
    "a!b",
    "a^b",
    "!a",
    "$HOME $((1+2))",
]

QUOTE_EXTRA = ["a/b-c.d_e:f,g@h%i+j", "~", "a#b", "a\x00b", "\U0001f600"]


def lex(mode, text):
    try:
        if mode == "split":
            toks = shlex.split(text)
        else:
            lx = shlex.shlex(text, posix=True, punctuation_chars=True)
            if mode == "script":
                lx.whitespace_split = True
                lx.commenters = ""
            toks = list(lx)
    except ValueError as e:
        return {"error": str(e)}
    return {"tokens": toks}


def corpus_commands():
    with open(CORPUS, encoding="utf-8") as f:
        d = json.load(f)
    out = []
    for key in ("must_block", "must_allow"):
        for e in d[key]:
            cmd = e if isinstance(e, str) else e["cmd"]
            if cmd not in out:
                out.append(cmd)
    return out


def build():
    cmds = corpus_commands()
    cases = []
    for src, items in (("corpus", cmds), ("edge", EDGES)):
        for text in items:
            for mode in LEX_MODES:
                cases.append({"src": src, "mode": mode, "input": text, **lex(mode, text)})
            cases.append({"src": src, "mode": "quote", "input": text, "quoted": shlex.quote(text)})
    for text in QUOTE_EXTRA:
        cases.append({"src": "edge", "mode": "quote", "input": text, "quoted": shlex.quote(text)})
    return {"python": PYTHON, "corpus_commands": len(cmds), "edge_inputs": len(EDGES), "cases": cases}


def render(doc):
    return json.dumps(doc, ensure_ascii=True, indent=0, sort_keys=True) + "\n"


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--check", action="store_true", help="exit 1 if the golden file is stale")
    args = ap.parse_args(argv)
    ver = "%d.%d" % sys.version_info[:2]
    if ver != PYTHON:
        print(f"shlex_golden: needs CPython {PYTHON} (the CI version), found {ver}; run it with python{PYTHON}", file=sys.stderr)
        return 2
    text = render(build())
    if args.check:
        try:
            with open(GOLDEN, encoding="utf-8") as f:
                cur = f.read()
        except FileNotFoundError:
            cur = None
        if cur != text:
            print("shlex_golden: go/internal/shlex/testdata/golden.json is stale; run python3 ci/shlex_golden.py and commit it", file=sys.stderr)
            return 1
        print("shlex_golden: up to date")
        return 0
    os.makedirs(os.path.dirname(GOLDEN), exist_ok=True)
    with open(GOLDEN, "w", encoding="utf-8") as f:
        f.write(text)
    print(f"shlex_golden: wrote {len(json.loads(text)['cases'])} cases")
    return 0


if __name__ == "__main__":
    sys.exit(main())
