#!/usr/bin/env python3
"""Golden data for the Go port of the worker-guard front end (go/internal/guard, G-016 T-0347).

Imports roles/hooks/worker-guard.py by path and runs its shell-text front end - preprocess, extract_subs,
mask_quotes and segments - over (a) every command in ci/guard_corpus.json and (b) a hand list of edge cases
(heredocs, quoting, comments, backticks, non-ASCII). The results go to
go/internal/guard/testdata/frontend_golden.json; the Go test asserts identical outputs.

  python3 ci/guard_golden.py           write the golden file
  python3 ci/guard_golden.py --check   exit 1 if the file is stale

The hook is the behaviour contract: when a guard change alters the front end, regenerate the golden in the
same PR and port the change to Go. Reference interpreter: CPython 3.13 (the version in .github/workflows/tests.yml);
another minor version is refused.
"""
import argparse
import importlib.util
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORPUS = os.path.join(ROOT, "ci", "guard_corpus.json")
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
GOLDEN = os.path.join(ROOT, "go", "internal", "guard", "testdata", "frontend_golden.json")
PYTHON = "3.13"

EDGES = [
    "",
    " ",
    "ls",
    "echo a # c\nls",
    "echo a#b",
    "echo 'a # b' # c",
    "echo \"a # b\"",
    "a\\\nb",
    "a \\\n  b",
    "echo `date`",
    "echo \"`date`\"",
    "echo '`date`'",
    "echo \"$(date)\"",
    "echo \"$(date\"",
    "echo $(date)",
    "echo \"a $(b \"$(c)\") d\"",
    "echo `a\\`b`",
    "echo `a",
    "echo 'a",
    "echo \"a",
    "echo a\\",
    "cat <<EOF\nhello\nEOF",
    "cat <<EOF\nhello\nEOF\nls",
    "cat <<-EOF\n\thello\n\tEOF\nls",
    "cat <<'EOF'\n$(sudo x)\nEOF\nls",
    "cat <<\"EOF\"\nbody\nEOF",
    "cat <<\\EOF\nbody\nEOF",
    "cat <<E\"O\"F\nbody\nEOF",
    "cat <<EOF",
    "cat <<EOF\nnever closed",
    "cat <<EOF\na \\\nEOF\nEOF\nls",
    "cat <<'EOF'\na \\\nEOF\nEOF\nls",
    "cat <<$(x)\nbody\nx",
    "cat <<`x`\nbody\nx",
    "cat <<$'x'\nbody\nx",
    "cat <<\"a b\"\nbody\na b\nls",
    "cat <<\"a\\\"b\"\nbody\na\"b\nls",
    "cat <<'a\nbody",
    "cat <<<'x'\nls",
    "bash <<EOF\nsudo ls\nEOF",
    "bash -c 'x' <<EOF\nsudo ls\nEOF",
    "eval <<EOF\nsudo ls\nEOF",
    "source /dev/stdin <<EOF\nsudo ls\nEOF",
    "python3 <<EOF\nimport os\nEOF",
    "python3.12 <<EOF\nimport os\nEOF",
    "python3 - <<EOF\nimport os\nEOF",
    "env python3 <<EOF\nx\nEOF",
    "cat file | python3 <<EOF\nx\nEOF",
    "ruby <<EOF\nx\nEOF",
    "node <<EOF\nx\nEOF",
    "nodejs <<EOF\nx\nEOF",
    "cat <<EOF | perl\nx\nEOF",
    "tee out <<EOF\nx\nEOF",
    "cat a b c d e f python <<EOF\nx\nEOF",
    "echo a; sed s/a/b/ <<EOF\nx\nEOF",
    "x=1 python3 <<EOF\nx\nEOF",
    "sed 's/a/b/' <<EOF\nx\nEOF",
    "cat <<A <<B\nbody a\nA\nbody b\nB\nls",
    "git commit -F - <<EOF\nmsg\nEOF",
    "echo $(cat <<EOF\nmsg\nEOF\n)",
    "sudo\nls",
    "a && b || c ; d | e",
    "a; (b; c) & d",
    "{ a; b; }",
    "a `b` c",
    "a $(b) c",
    "a $((1+2)) c",
    "a ${b} c",
    "echo 'a;b' ; echo \"c|d\" | e",
    "echo 'a\nb' ; c",
    "echo \"a\nb\" ; c",
    "echo \"a\\\"b\" ; c",
    "echo \"a\\\\\" ; c",
    "echo \\; ; c",
    "echo \\' ; c",
    "echo a\\\n; b",
    "# only a comment",
    "ls # trailing",
    "ls;# c",
    "ls &# c",
    "ls |# c",
    "ls (# c",
    "echo $#",
    "echo ${#a}",
    "\t ls \n\n  pwd\n",
    "ls\r\npwd",
    "ls\x0bpwd",
    "ls\x1cpwd",
    "echo ünï 'çödé' ; echo \"日本語 $(x)\"",
    "echo 'é' | e",
    "echo \"é\\é\" ; é",
    "cat <<É\nbody\nÉ\nls",
    "cat <<EOF\nünï\nEOF\nls ünï",
    "python3 <<EOF\nsudoé\nEOF",
    "echo 日本語herdr",
    "echo  x y　z",
    "echo a b",
]


def load_hook():
    spec = importlib.util.spec_from_file_location("worker_guard_hook", HOOK)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


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


def attempt(hook, fn):
    try:
        return {"ok": fn()}
    except hook.Block as e:
        return {"error": str(e)}


def case(hook, cmd):
    """Every front-end output for one command."""
    out = {"input": cmd}
    for fold in (False, True):
        bodies = []
        r = attempt(hook, lambda: hook.preprocess(cmd, fold, bodies))
        if "ok" in r:
            text, extras = r["ok"]
            r = {"text": text, "extras": [list(x) for x in extras], "bodies": [list(b) for b in bodies]}
        out["preprocess_fold" if fold else "preprocess"] = r
    pre = out["preprocess"].get("text")
    for name, text in (("raw", cmd), ("pre", pre)):
        if text is None:
            continue
        r = attempt(hook, lambda: hook.extract_subs(text))
        out["extract_subs_" + name] = {"subs": r["ok"]} if "ok" in r else r
        out["mask_quotes_" + name] = hook.mask_quotes(text)
        out["segments_" + name] = [[s, e, w] for s, e, w in hook.segments(text)]
    return out


CONST_SETS = ["FACTORY_UNIT_CMDS", "PRIV_WORDS", "WRAP_DATA", "PERSIST_EDIT_TOOLS", "TERMS_ROLES", "SHELLS", "WRAPPERS",
              "RESERVED", "SYSTEMCTL_VERBS", "UNIT_MUTATING", "SYSTEMCTL_NOUNIT", "COPIERS", "DATA_CMDS", "LAUNCHERS",
              "FIND_EXEC", "AWKS", "EDITORS", "DECLARERS", "SCRUB_VARS", "CFG_WRITERS", "CFG_REMOVERS", "REMOTE_WRITE",
              "CONFIG_VALUE_OPTS", "GIT_VALUE_OPTS", "GIT_READ_ONLY", "BRANCH_LIST_LONG", "BRANCH_PLAIN_LONG",
              "BRANCH_VALUE_LONG", "CONFIG_READ", "CONFIG_WRITE", "GH_VALUE_OPTS", "GH_MUTATING", "GH_REPO_VERBS",
              "GIT_CMD_KEYS", "PY_FLAGS"]
CONST_TUPLES = ["IDENT_VARS", "FACTORY_NAMES", "PERSIST_NAMES"]
CONST_SET_MAPS = ["BROAD_READS", "WRAP_VALUE_OPTS"]


def constants(hook):
    """The module constants the Go port repeats, in JSON form."""
    out = {"MAX_DEPTH": hook.MAX_DEPTH, "CLAUDE_PKG": hook.CLAUDE_PKG, "HEREDOC_WORD_END": hook.HEREDOC_WORD_END}
    for n in CONST_SETS:
        out[n] = sorted(getattr(hook, n))
    for n in CONST_TUPLES:
        out[n] = list(getattr(hook, n))
    for n in CONST_SET_MAPS:
        out[n] = {k: (None if v is None else sorted(v)) for k, v in getattr(hook, n).items()}
    out["INTERP_OPTS"] = {k: {f: sorted(v) for f, v in spec.items()} for k, spec in hook.INTERP_OPTS.items()}
    return out


def build():
    hook = load_hook()
    cmds = corpus_commands()
    cases = []
    for src, items in (("corpus", cmds), ("edge", EDGES)):
        for text in items:
            c = case(hook, text)
            c["src"] = src
            cases.append(c)
    return {"python": PYTHON, "corpus_commands": len(cmds), "edge_inputs": len(EDGES), "constants": constants(hook),
            "cases": cases}


def render(doc):
    return json.dumps(doc, ensure_ascii=True, indent=0, sort_keys=True) + "\n"


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--check", action="store_true", help="exit 1 if the golden file is stale")
    args = ap.parse_args(argv)
    ver = "%d.%d" % sys.version_info[:2]
    if ver != PYTHON:
        print(f"guard_golden: needs CPython {PYTHON} (the CI version), found {ver}; run it with python{PYTHON}", file=sys.stderr)
        return 2
    text = render(build())
    if args.check:
        try:
            with open(GOLDEN, encoding="utf-8") as f:
                have = f.read()
        except FileNotFoundError:
            have = None
        if have != text:
            print("guard_golden: go/internal/guard/testdata/frontend_golden.json is stale; run: python3 ci/guard_golden.py",
                  file=sys.stderr)
            return 1
        print("guard_golden: golden is current")
        return 0
    os.makedirs(os.path.dirname(GOLDEN), exist_ok=True)
    with open(GOLDEN, "w", encoding="utf-8") as f:
        f.write(text)
    print(f"guard_golden: wrote {len(text)} bytes, {len(corpus_commands())} corpus commands + {len(EDGES)} edge inputs")
    return 0


if __name__ == "__main__":
    sys.exit(main())
