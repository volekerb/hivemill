#!/usr/bin/env python3
"""One-off helper for T-1083: merge git config classifier forms into ci/guard_corpus.json. Idempotent."""
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORPUS_PATH = os.path.join(ROOT, "ci", "guard_corpus.json")
sys.path.insert(0, os.path.join(ROOT, "tests"))

import test_worker_guard_config_oracle as oracle  # noqa: E402
import test_worker_guard_config_value_opts as value_opts  # noqa: E402
import test_worker_guard_config_reads as reads  # noqa: E402
import test_worker_guard_config_order as order  # noqa: E402
from guard_impl import hook_run  # noqa: E402


def hook_rc(cmd, role="coder", env_extra=None):
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": "/tmp"})
    env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
    if env_extra:
        env.update(env_extra)
    return hook_run(payload, ["--role", role], env=env).returncode


def cmd_key(e):
    if isinstance(e, str):
        return ("str", e)
    return ("obj", e.get("cmd", ""), e.get("role"), tuple(sorted((e.get("env") or {}).items())))


def existing_keys(doc):
    out = set()
    for key in ("must_block", "must_allow"):
        for e in doc[key]:
            out.add(cmd_key(e))
    return out


def add_block(doc, seen, cmd, **extra):
    e = {"cmd": cmd, **extra} if extra else cmd
    k = cmd_key(e)
    if k in seen:
        return
    doc["must_block"].append(e if extra else cmd)
    seen.add(k)


def add_allow(doc, seen, cmd, **extra):
    e = {"cmd": cmd, **extra} if extra else cmd
    k = cmd_key(e)
    if k in seen:
        return
    doc["must_allow"].append(e if extra else cmd)
    seen.add(k)


def main():
    with open(CORPUS_PATH, encoding="utf-8") as f:
        doc = json.load(f)
    seen = existing_keys(doc)

    for cmd in value_opts.DENIED + value_opts.CODER_ONLY_DENIED:
        add_block(doc, seen, cmd)
    for cmd in value_opts.ALLOWED:
        add_allow(doc, seen, cmd)

    for cmd in reads.EVERY_ROLE_READS + reads.WIDE_ROLE_READS:
        add_allow(doc, seen, "git config " + cmd if not cmd.startswith("git ") else cmd)
    for role in ("reviewer", "qa", "pm"):
        for cmd in reads.WIDE_ROLE_READS:
            c = "git config " + cmd if not cmd.startswith("git ") else cmd
            add_block(doc, seen, c, role=role)
    for cmd in reads.REFUSED:
        add_block(doc, seen, "git config " + cmd if not cmd.startswith("git ") else cmd)
    add_allow(doc, seen, "echo alias.x | xargs git config --get --show-origin")
    add_block(doc, seen, "echo alias.x push | xargs git config --get-color --unset")
    for cmd in ("git config -t bool alias.x", "git config --value x alias.y"):
        add_allow(doc, seen, cmd)

    for tail in order.TIGHTENED + order.STILL_REFUSED:
        add_block(doc, seen, "git config " + tail)
    for tail in order.READS:
        add_allow(doc, seen, "git config " + tail)
    scratch = "@SCRATCH@"
    for pre in order.SCRATCH_PREFIXES:
        for tok in order.SCRATCH_TOKENS:
            form = f"{pre}core.hooksPath /tmp/x {tok}".replace("<S>", scratch).strip()
            add_block(doc, seen, "git config " + form)
    for f in ("--typ bool --global core.hooksPath /tmp/x", "--def x --global core.hooksPath /tmp/x",
              "--comm x --global core.hooksPath /tmp/x", "--blo x --glob core.hooksPath /tmp/x",
              "--type bool --global core.hooksPath /tmp/x", "--add --global core.hooksPath /tmp/x"):
        add_block(doc, seen, "git config " + f)

    import tempfile
    tmp_scratch = tempfile.mkdtemp(prefix="corpus-scratch-")
    for form in oracle.CORPUS + oracle.CROSS + oracle.LATE_FORMS:
        filled = [t.replace("<S>", scratch) for t in form]
        cmd = "git config " + " ".join(filled)
        classify_cmd = "git config " + " ".join(t.replace("<S>", tmp_scratch) for t in form)
        env_extra = {"WORKER_GUARD_SCRATCH": tmp_scratch} if any("<S>" in t for t in form) else None
        rc = hook_rc(classify_cmd, env_extra=env_extra)
        if rc == 2:
            add_block(doc, seen, cmd)
        elif rc == 0:
            add_allow(doc, seen, cmd)
        else:
            raise SystemExit(f"unexpected rc={rc} for {cmd!r}")

    with open(CORPUS_PATH, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=True, indent=1)
        f.write("\n")
    print(f"corpus must_block={len(doc['must_block'])} must_allow={len(doc['must_allow'])}")


if __name__ == "__main__":
    main()
