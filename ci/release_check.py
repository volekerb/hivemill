#!/usr/bin/env python3
"""Release gate: runs every check on a release candidate SHA and prints the owner's tag commands when all pass.

  python3 ci/release_check.py <sha> [--version X.Y.Z] [--repo OWNER/NAME]

Checks (one PASS/FAIL line each, all run): on-main, version, ci-green, notes, fresh-install-smoke, public-text, tag-free.
Exit 0 all pass (tag commands printed), 1 a check failed, 2 usage error or unresolvable SHA.
Read-only: it never tags, pushes or creates a release; it only prints the commands for the owner. See docs/RELEASING.md.
Python 3 stdlib only.
"""
import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+$")
PLACEHOLDER_RES = (re.compile(r"TODO|TBD|FIXME|XXX|__[A-Z_]+__|<[a-z][a-z -]*>|<!--"),)
SMOKE_VERSION_RE = re.compile(r"^hivemill (\S+)(?:\s|$)")
REMOTE_RE = re.compile(r"^(?:https://github\.com/|git@github\.com:)([^/\s]+)/([^/\s]+?)(?:\.git)?/?$")
SMOKE_WAIT_S = 20


class UsageError(Exception):
    pass


def run_cmd(argv, cwd=None, env=None, timeout=300):
    """Run argv; return (returncode, stdout, stderr). A missing program or a timeout is returncode 127 with the reason."""
    try:
        p = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    except OSError as e:
        return 127, "", f"cannot run {argv[0]}: {e.strerror}"
    except subprocess.TimeoutExpired:
        return 124, "", f"{argv[0]} timed out after {timeout}s"
    return p.returncode, p.stdout, p.stderr


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def serve_probe(argv, env, port):
    """Start `argv` (factory serve), wait for HTTP 200 on /, stop it. Return (ok, detail)."""
    proc = subprocess.Popen(argv, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        deadline = time.monotonic() + SMOKE_WAIT_S
        last = "no answer"
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                return False, f"factory serve exited with {proc.returncode}"
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=3) as r:
                    return r.status == 200, f"HTTP {r.status} on /"
            except urllib.error.HTTPError as e:
                return False, f"HTTP {e.code} on /"
            except (urllib.error.URLError, OSError) as e:
                last = str(getattr(e, "reason", e))
                time.sleep(0.3)
        return False, f"no HTTP 200 on / within {SMOKE_WAIT_S}s ({last})"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


class Gate:
    def __init__(self, sha, version, repo, run=run_cmd, probe=serve_probe, root=ROOT, env=None):
        self.sha, self.version, self.repo = sha, version, repo
        self.run, self.probe, self.root = run, probe, str(root)
        self.env = dict(os.environ if env is None else env)
        self.tmp = None

    def git(self, *args):
        return self.run(["git", "-C", self.root, *args])

    # --- checks: each returns (ok, detail) ---

    def on_main(self):
        rc, out, err = self.git("rev-list", "--first-parent", "origin/main")
        if rc != 0:
            return False, f"cannot list origin/main ({err.strip() or rc}). Run: git fetch origin"
        if self.sha in out.split():
            return True, f"{self.sha[:12]} is on the first-parent history of origin/main"
        return False, f"{self.sha[:12]} is not on the first-parent history of origin/main. Pick a main commit (git fetch origin first)"

    def version_check(self):
        if not VERSION_RE.match(self.version):
            return False, f"{self.version!r} is not MAJOR.MINOR.PATCH"
        rc, out, err = self.git("show", f"{self.sha}:VERSION")
        if rc != 0:
            return False, f"no VERSION file at {self.sha[:12]} ({err.strip() or rc})"
        have = out.strip()
        if have != self.version:
            return False, f"VERSION at {self.sha[:12]} is {have!r}, not {self.version!r}"
        return True, f"VERSION {have}"

    def ci_green(self):
        rc, out, err = self.run(["gh", "run", "list", "-R", self.repo, "--workflow", "tests.yml", "--commit", self.sha,
                                 "--event", 'push', "--json", "status,conclusion,databaseId"])
        if rc != 0:
            return False, f"gh run list failed ({err.strip() or rc})"
        try:
            runs = json.loads(out or "[]")
        except ValueError:
            return False, "gh run list printed invalid JSON"
        for r in runs:
            if r.get("status") == "completed" and r.get("conclusion") == "success":
                return True, f"tests.yml push run {r.get('databaseId')} succeeded (push runs on main run the full suite)"
        if not runs:
            return False, f"no tests.yml push run for {self.sha[:12]}; wait for CI on main"
        return False, "no successful tests.yml push run: " + ", ".join(
            f"{r.get('databaseId')} {r.get('status')}/{r.get('conclusion') or '-'}" for r in runs)

    def notes(self):
        src = os.path.join(self.tmp, "src")
        rc, out, err = self.run([sys.executable, "ci/changelog.py", "--check-version", self.version], cwd=src)
        if rc != 0:
            return False, f"changelog.py --check-version {self.version} exit {rc}: {(err or out).strip()}"
        rc, out, err = self.run([sys.executable, "ci/changelog.py", "--release-notes", self.version], cwd=src)
        if rc != 0:
            return False, f"changelog.py --release-notes {self.version} exit {rc}: {(err or out).strip()}"
        hits = []
        for n, line in enumerate(out.splitlines(), 1):
            for rx in PLACEHOLDER_RES:
                for m in rx.finditer(line):
                    hits.append(f"line {n}: {m.group(0)}")
        if hits:
            return False, "placeholders in the release notes: " + "; ".join(hits)
        return True, f"notes for {self.version} complete, 0 placeholders"

    def smoke(self):
        """Fresh install from a detached checkout, isolated HOME and FACTORY_HOME under the temp dir."""
        src = os.path.join(self.tmp, "src")
        home, fh, demo = (os.path.join(self.tmp, d) for d in ("home", "fh", "demo"))
        os.makedirs(home)
        os.makedirs(fh)
        env = {k: v for k, v in self.env.items() if k not in ("HOME", "FACTORY_HOME")}
        env.update(HOME=home, FACTORY_HOME=fh)
        factory = [sys.executable, os.path.join(src, "bin", "factory")]
        rc, out, err = self.run(factory + ["--version"], env=env)
        want = f"hivemill {self.version}"
        m = SMOKE_VERSION_RE.match(out)
        if rc != 0 or not m or m.group(1) != self.version:
            return False, f"factory --version printed {out.strip()!r} (exit {rc}), want version exactly {self.version!r} ({want!r}). {err.strip()}"
        rc, out, err = self.run([sys.executable, os.path.join(src, "ci", "demo_seed.py"), "--home", demo], env=env)
        if rc != 0:
            return False, f"demo_seed.py exit {rc}: {(err or out).strip()[-300:]}"
        port = free_port()
        senv = dict(env, FACTORY_HOME=demo)
        ok, detail = self.probe(factory + ["serve", "--bind", "127.0.0.1", "--port", str(port)], senv, port)
        if not ok:
            return False, f"factory serve on the demo home: {detail}"
        return True, f"factory --version, demo_seed.py and factory serve ({detail}) from a detached checkout"

    def public_text(self):
        src = os.path.join(self.tmp, "src")
        notes = os.path.join(src, "ci", "release-notes", f"{self.version}.md")
        rc, out, err = self.run([sys.executable, os.path.join(src, "ci", "public_text_guard.py"), "text", notes], env=self.env)
        if rc == 0:
            return True, f"public_text_guard.py text ci/release-notes/{self.version}.md is clean"
        why = "owner terms not loaded, the guard fails closed" if rc == 2 else "hits"
        return False, f"public_text_guard.py exit {rc} ({why}): {(err or out).strip()[-300:]}"

    def tag_free(self):
        tag = f"v{self.version}"
        rc, out, err = self.git("ls-remote", "--tags", "origin", f"refs/tags/{tag}")
        if rc != 0:
            return False, f"git ls-remote failed ({err.strip() or rc})"
        if out.strip():
            return False, f"tag {tag} already exists on origin"
        return True, f"{tag} is not on origin"

    # --- driver ---

    def prepare_checkout(self):
        src = os.path.join(self.tmp, "src")
        rc, _, err = self.run(["git", "clone", "--quiet", "--no-checkout", "--shared", self.root, src])
        if rc == 0:
            rc, _, err = self.run(["git", "-C", src, "checkout", "--quiet", "--detach", self.sha])
        return None if rc == 0 else f"cannot make a detached checkout of {self.sha[:12]}: {err.strip() or rc}"

    def run_all(self, out):
        self.tmp = tempfile.mkdtemp(prefix="release-check-")
        try:
            broken = self.prepare_checkout()
            needs_src = {"notes", "fresh-install-smoke", "public-text"}
            results = []
            for name, fn in (("on-main", self.on_main), ("version", self.version_check), ("ci-green", self.ci_green),
                             ("notes", self.notes), ("fresh-install-smoke", self.smoke), ("public-text", self.public_text),
                             ("tag-free", self.tag_free)):
                ok, detail = (False, broken) if broken and name in needs_src else fn()
                out(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
                results.append(ok)
        finally:
            shutil.rmtree(self.tmp, ignore_errors=True)
        return all(results)

    def owner_commands(self):
        v = self.version
        lines = []
        if v.split(".")[2] == "0":
            lines += [f"Reminder: v{v} is a MINOR (or MAJOR) release. Before tagging, the owner also runs the full fresh-box test:",
                      f"  cd {self.root} && sudo python3 ci/freshbox_test.py"]
        lines += [f"Release v{v} is ready. Owner, run:",
                  "  git -C ~/factory fetch origin",
                  f"  git -C ~/factory tag -a v{v} -m v{v} {self.sha}",
                  f"  git -C ~/factory push origin v{v}",
                  "Do not run `gh release create`: the tag push makes release.yml create the release."]
        return lines


def parse_args(argv):
    p = argparse.ArgumentParser(prog="release_check.py", description="Release gate for a candidate SHA on origin/main.")
    p.add_argument("sha", help="commit-ish of the release candidate")
    p.add_argument("--version", help="X.Y.Z (default: VERSION at the SHA)")
    p.add_argument("--repo", help="OWNER/NAME (default: parsed from the origin remote)")
    try:
        return p.parse_args(argv)
    except SystemExit as e:
        raise UsageError("") if e.code == 0 else UsageError("usage error") from None


def main(argv=None, run=run_cmd, probe=serve_probe, root=ROOT, env=None, out=print):
    try:
        args = parse_args(sys.argv[1:] if argv is None else argv)
    except UsageError:
        return 2
    rc, full, err = run(["git", "-C", str(root), "rev-parse", "--verify", f"{args.sha}^{{commit}}"])
    full = full.strip()
    if rc != 0 or not re.fullmatch(r"[0-9a-f]{40}", full):
        print(f"release_check: cannot resolve {args.sha!r} to a commit ({err.strip() or 'no output'}). Run: git fetch origin", file=sys.stderr)
        return 2
    version = args.version
    if version is None:
        rc, vout, verr = run(["git", "-C", str(root), "show", f"{full}:VERSION"])
        version = vout.strip() if rc == 0 else ""
    repo = args.repo
    if repo is None:
        rc, url, _ = run(["git", "-C", str(root), "remote", "get-url", "origin"])
        m = REMOTE_RE.match(url.strip()) if rc == 0 else None
        if not m:
            print("release_check: cannot parse OWNER/NAME from the origin remote. Pass --repo OWNER/NAME.", file=sys.stderr)
            return 2
        repo = f"{m.group(1)}/{m.group(2)}"
    gate = Gate(full, version, repo, run=run, probe=probe, root=root, env=env)
    if not gate.run_all(out):
        return 1
    for line in gate.owner_commands():
        out(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
