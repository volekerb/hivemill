"""Skill scout fetch: the five sources, dedupe, star snapshots and install-risk (plan G-009 §4, §5, §7.1).

Every GitHub call goes through Gh.api (`gh api` only, 30 s timeout). Nothing fetched is executed or
written to disk; READMEs and descriptions are untrusted text that only ends up in bound SQL parameters.
A failing source is caught and reported in the summary; the others still run.
"""
import datetime as dt
import hashlib
import json
import re
import sqlite3
import subprocess
import urllib.error
import urllib.parse
import urllib.request

from . import db as scoutdb
from . import risk

GH_TIMEOUT = 30
HN_TIMEOUT = 15
HN_DEFAULT_URL = "https://hn.algolia.com/api/v1/search_by_date"
HN_QUERIES = ("claude code skill", "claude skills", "claude code plugin")
HN_MIN_POINTS = 20
HN_FIRST_LOOKBACK_DAYS = 3
MAX_SEARCH_CALLS = 10
NEW_CAP = 15             # new candidates per run from search + awesome-lists + hn
MAX_CHECKS = 40          # unknown repos whose tree is inspected per run (calls budget)
DROPPED_MAX = 2000
TOPICS = ("claude-code", "claude-skills", "agent-skills", "claude-code-plugin", "claude-code-skills",
          "claude-code-subagents")
MARKETPLACE_REPO = "anthropics/claude-plugins-official"
SKILLS_REPO = "anthropics/skills"
AWESOME = ("hesreallyhim/awesome-claude-code", "ComposioHQ/awesome-claude-skills",
           "travisvn/awesome-claude-skills", "VoltAgent/awesome-claude-code-subagents",
           "zhuyansen/awesome-claude-video-skills")
RAW = ("-H", "Accept: application/vnd.github.raw")
README_MAX = 4096
SOURCES = ("marketplace", "skills", "search", "awesome", "hn")

REPO_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
PATH_RE = re.compile(r"^[A-Za-z0-9_.@+/ -]+$")
LINK_RE = re.compile(r"https?://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)", re.I)
NOT_OWNERS = {"sponsors", "topics", "orgs", "features", "about", "settings", "marketplace", "apps", "login", "join",
              "pricing", "collections", "trending", "explore", "enterprise", "security", "readme", "site",
              "customer-stories", "user-attachments", "issues", "pulls", "notifications", "search"}


class GhError(Exception):
    """A gh call failed, timed out or returned something unusable."""


class Gh:
    """The one door to GitHub: `gh api <args>` with a timeout. Counts calls for the summary."""

    def __init__(self, timeout=GH_TIMEOUT):
        self.timeout, self.calls, self.search_calls = timeout, 0, 0

    def api(self, *args, search=False):
        if search:
            if self.search_calls >= MAX_SEARCH_CALLS:
                raise GhError(f"search call cap ({MAX_SEARCH_CALLS}) reached")
            self.search_calls += 1
        self.calls += 1
        what = next((a for a in args if not a.startswith("-") and "/" in a), args[0] if args else "?")
        try:
            p = subprocess.run(["gh", "api", *args], capture_output=True, text=True, timeout=self.timeout)
        except subprocess.TimeoutExpired:
            raise GhError(f"gh api {what}: timed out after {self.timeout} s") from None
        except FileNotFoundError:
            raise GhError("gh not found on PATH: install the GitHub CLI") from None
        if p.returncode != 0:
            line = (p.stderr.strip().splitlines() or ["no output"])[0][:160]
            raise GhError(f"gh api {what}: exit {p.returncode}: {line}")
        return p.stdout

    def json(self, *args, search=False):
        out = self.api(*args, search=search)
        try:
            return json.loads(out)
        except ValueError:
            raise GhError(f"gh api {args[0]}: response is not JSON") from None


def gh_key(repo, path=None):
    return f"gh:{repo}" + (f"/{path.strip('/')}" if path else "")


def readme_excerpt(gh, repo, path, sha):
    """Up to README_MAX bytes of the README (or SKILL.md) of a candidate, fetched at its head sha.
    Returns (text, error); one is None."""
    if not REPO_RE.match(repo or "") or not re.fullmatch(r"[0-9a-f]{7,64}", sha or ""):
        return None, "no repo or head sha recorded"
    base = f"repos/{repo}/contents/"
    ends = [f"{path.strip('/')}/README.md", f"{path.strip('/')}/SKILL.md"] if path and PATH_RE.match(path) else []
    for end in ends:
        try:
            return _clip(gh.api(f"{base}{end}?ref={sha}", *RAW)), None
        except GhError:
            continue  # fall back to the repo README
    try:
        return _clip(gh.api(f"repos/{repo}/readme?ref={sha}", *RAW)), None
    except GhError as e:
        return None, str(e)


def _clip(text):
    return text.encode("utf-8")[:README_MAX].decode("utf-8", "ignore")


class Run:
    """State of one fetch: db, gh, clock, caches, counters."""

    def __init__(self, con, gh, now, hn_url=None):
        self.con, self.gh, self.now = con, gh, now
        self.ts = now.isoformat(timespec="seconds")
        self.hn_url = hn_url or HN_DEFAULT_URL
        self.hn_calls = 0
        self.stats = {s: {"new": 0, "updated": 0} for s in SOURCES}
        self.errors = []
        self.new_found = 0       # discovery pool (search + awesome + hn), capped at NEW_CAP
        self.checks = 0
        self._repo, self._head, self._tree = {}, {}, {}
        self.dropped = {}        # repos without a manifest: repo -> pushed_at ('' when unknown)
        self.soft = {}           # source -> messages of single candidates that failed (the source went on)

    # -- cached GitHub reads ---------------------------------------------------------------
    def repo_info(self, repo, item=None):
        if repo not in self._repo:
            self._repo[repo] = item if item is not None else self.gh.json(f"repos/{repo}")
        return self._repo[repo]

    def head(self, repo, branch):
        if repo not in self._head:
            sha = self.gh.api(f"repos/{repo}/commits/{branch}", "--jq", ".sha").strip()
            if not re.fullmatch(r"[0-9a-f]{40}", sha):
                raise GhError(f"repos/{repo}: head commit of {branch} is not a sha")
            self._head[repo] = sha
        return self._head[repo]

    def tree(self, repo, sha):
        if (repo, sha) not in self._tree:
            data = self.gh.json(f"repos/{repo}/git/trees/{sha}?recursive=1")
            tree = data.get("tree")
            if not isinstance(tree, list):
                raise GhError(f"repos/{repo}: tree response has no tree list")
            self._tree[(repo, sha)] = (tree, bool(data.get("truncated")))
        return self._tree[(repo, sha)]

    def known(self, key):
        return self.con.execute("SELECT id, status FROM scout_candidates WHERE key=?",
                                (scoutdb.normalize_key(key),)).fetchone()

    def count(self, source, what):
        self.stats[source][what] += 1


# -- candidate pipeline ----------------------------------------------------------------------
def _prepare(run, repo, path, item=None):
    info = run.repo_info(repo, item)
    sha = run.head(repo, info.get("default_branch") or "main")
    tree, truncated = run.tree(repo, sha)
    return info, sha, tree, truncated


def _facts(run, repo, path, prep):
    info, sha, tree, truncated = prep
    prefix = (path or "").strip("/")

    def read(entry):
        try:
            return run.gh.api(f"repos/{repo}/git/blobs/{entry['sha']}", *RAW)
        except (GhError, KeyError) as e:
            raise risk.ReadError(str(e)) from e

    res = risk.classify(tree, prefix, read, truncated)
    branch = info.get("default_branch") or "main"
    return {
        "kind": res.kind, "repo": repo, "path": prefix or None,
        "url": f"https://github.com/{repo}" + (f"/tree/{branch}/{prefix}" if prefix else ""),
        "licence": (info.get("license") or {}).get("spdx_id"),
        "last_commit": info.get("pushed_at"), "default_branch": info.get("default_branch"), "head_sha": sha,
        "stars": info.get("stargazers_count"), "install_risk": res.risk, "risk_detail": res.detail,
    }


NEW, UPDATED, DROPPED, DEFERRED, FAILED = "new", "updated", "dropped", "deferred", "failed"


def consider(run, source, repo, path=None, item=None, force=False, discovery=False):
    """One sighting of a GitHub candidate. A known key only gets its source merged, seen_count and (when the
    sighting carries a star count) a star snapshot, unless `force`. An unknown key is inspected: repo facts,
    head sha, one tree call, install risk. `discovery` sources must find a manifest and share the NEW_CAP and
    MAX_CHECKS budgets. Returns NEW, UPDATED, DROPPED or DEFERRED (budget spent; not stored)."""
    if not REPO_RE.match(repo) or (path and (".." in path or not PATH_RE.match(path))):
        raise GhError(f"refusing odd repo/path {repo!r} {path!r}")
    key = gh_key(repo, path)
    row = run.known(key)
    if row and not force:
        cid, _ = scoutdb.upsert_candidate(run.con, key, source, ts=run.ts)
        stars = (item or {}).get("stargazers_count")
        if stars is not None:
            scoutdb.snapshot_stars(run.con, cid, stars, run.ts)
        run.count(source, "updated")
        return UPDATED
    if discovery and not row:
        if run.new_found >= NEW_CAP or run.checks >= MAX_CHECKS:
            return DEFERRED
        memo = run.dropped.get(repo.lower())
        pushed = (item or {}).get("pushed_at")
        if memo is not None and (pushed is None or pushed == memo):
            return DROPPED
        run.checks += 1
    prep = _prepare(run, repo, path, item)
    if discovery and not row and not risk.has_manifest(prep[2], path):
        run.dropped[repo.lower()] = prep[0].get("pushed_at") or ""
        return DROPPED
    facts = _facts(run, repo, path, prep)
    cid, created = scoutdb.upsert_candidate(run.con, key, source, ts=run.ts, **facts)
    if facts["stars"] is not None:
        scoutdb.snapshot_stars(run.con, cid, facts["stars"], run.ts)
    run.count(source, "new" if created else "updated")
    if created and discovery:
        run.new_found += 1
    return NEW if created else UPDATED


def visit(run, source, repo, path=None, **kw):
    """consider() that survives one bad candidate (deleted repo, empty repo, odd link): the failure is
    recorded for the summary and the source goes on with the next one."""
    try:
        return consider(run, source, repo, path, **kw)
    except GhError as e:
        run.soft.setdefault(source, []).append(f"{repo}{'/' + path if path else ''}: {e}")
        return FAILED


def _cursor_json(run, source):
    raw = scoutdb.get_cursor(run.con, source)
    try:
        data = json.loads(raw) if raw else {}
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}


def _set_cursor(run, source, data):
    scoutdb.set_cursor(run.con, source, json.dumps(data, sort_keys=True), run.ts)


# -- a. anthropics/claude-plugins-official --------------------------------------------------
def _fingerprint(name, source):
    return hashlib.sha256(json.dumps([name, source], sort_keys=True).encode()).hexdigest()[:16]


def _github_repo(url):
    m = LINK_RE.match(url or "")
    if not m:
        return None
    repo = m.group(2)
    return f"{m.group(1)}/{repo[:-4] if repo.endswith('.git') else repo}"


def entry_target(name, source):
    """(repo, path) for a marketplace entry that is a GitHub repo or a path in the marketplace repo, else None."""
    if isinstance(source, str):
        p = source.strip()
        if p.startswith(("./", "../")) or p in (".", ""):
            while p.startswith("./"):
                p = p[2:]
            p = p.strip("/")
            if p == "." or ".." in p.split("/"):
                return None
            return MARKETPLACE_REPO, p or None
        return None
    if isinstance(source, dict):
        repo = source.get("repo") if REPO_RE.match(str(source.get("repo") or "")) else None
        repo = repo or _github_repo(source.get("url") or "")
        if repo:
            sub = source.get("path")
            return repo, (sub.strip("/") if isinstance(sub, str) and sub.strip("/") else None)
    return None


def src_marketplace(run):
    cur = _cursor_json(run, "plugins-official")
    sha = run.gh.api(f"repos/{MARKETPLACE_REPO}/commits/main", "--jq", ".sha").strip()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise GhError(f"{MARKETPLACE_REPO}: head commit is not a sha")
    if sha == cur.get("sha"):
        return
    data = run.gh.json(f"repos/{MARKETPLACE_REPO}/contents/.claude-plugin/marketplace.json?ref={sha}", *RAW)
    plugins = data.get("plugins") if isinstance(data, dict) else None
    if not isinstance(plugins, list):
        raise GhError("marketplace.json has no plugins list")
    entries = {}
    for p in plugins:
        if isinstance(p, dict) and isinstance(p.get("name"), str):
            entries[p["name"]] = (p.get("source"), _fingerprint(p["name"], p.get("source")))
    before = cur.get("entries", {})
    for name in sorted(entries):
        source, fp = entries[name]
        if before.get(name) == fp:
            continue
        target = entry_target(name, source)
        if target:
            visit(run, "marketplace", target[0], target[1], force=name in before)
        else:
            url = source.get("url") if isinstance(source, dict) and isinstance(source.get("url"), str) else None
            _, created = scoutdb.upsert_candidate(
                run.con, f"mkt:claude-plugins-official/{name}", "marketplace", ts=run.ts, kind="plugin",
                install_risk="unknown", url=url if url and url.startswith("http") else None)
            run.count("marketplace", "new" if created else "updated")
    if run.soft.get("marketplace"):
        return  # keep the old cursor: the failed entries are retried next run
    hashes = dict(list(cur.get("hashes", {}).items())[-4:])
    hashes[sha] = hashlib.sha256(json.dumps(sorted((n, v[1]) for n, v in entries.items())).encode()).hexdigest()[:16]
    _set_cursor(run, "plugins-official", {"sha": sha, "entries": {n: v[1] for n, v in entries.items()},
                                          "hashes": hashes})


# -- b. anthropics/skills ---------------------------------------------------------------------
def src_skills(run):
    cur = _cursor_json(run, "anthropics-skills")
    sha = run.gh.api(f"repos/{SKILLS_REPO}/commits/main", "--jq", ".sha").strip()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise GhError(f"{SKILLS_REPO}: head commit is not a sha")
    if sha == cur.get("sha"):
        return
    tree, _ = run.tree(SKILLS_REPO, sha)
    run._head[SKILLS_REPO] = sha  # same commit: the facts for each new skill reuse this tree
    names = sorted({e["path"].split("/")[1] for e in tree
                    if re.fullmatch(r"skills/[^/]+/SKILL\.md", e["path"]) and e.get("type") != "tree"})
    for name in names:
        if not run.known(gh_key(SKILLS_REPO, f"skills/{name}")):
            visit(run, "skills", SKILLS_REPO, f"skills/{name}")
    if run.soft.get("skills"):
        return
    _set_cursor(run, "anthropics-skills", {"sha": sha})


# -- c. GitHub repository search --------------------------------------------------------------
def search_queries(now):
    d14 = (now - dt.timedelta(days=14)).date().isoformat()
    d2 = (now - dt.timedelta(days=2)).date().isoformat()
    return [f"topic:{t} {flt}" for flt in (f"created:>={d14} stars:>=25", f"pushed:>={d2} stars:>=200")
            for t in TOPICS]


def src_search(run):
    cur = _cursor_json(run, "gh-search")
    queries = search_queries(run.now)
    off = int(cur.get("qoff", 0)) % len(queries)
    chosen = [queries[(off + i) % len(queries)] for i in range(min(MAX_SEARCH_CALLS, len(queries)))]
    found = {}
    for q in chosen:
        data = run.gh.json("-X", "GET", "search/repositories", "-f", f"q={q}", "-f", "sort=stars",
                           "-f", "per_page=30", search=True)
        for it in data.get("items", []):
            if isinstance(it, dict) and REPO_RE.match(str(it.get("full_name") or "")):
                found.setdefault(it["full_name"].lower(), it)
    for it in sorted(found.values(), key=lambda i: -(i.get("stargazers_count") or 0)):
        visit(run, "search", it["full_name"], item=it, discovery=True)
    _set_cursor(run, "gh-search", {"qoff": (off + len(chosen)) % len(queries)})


# -- d. awesome lists -------------------------------------------------------------------------
def readme_links(text, own):
    """Lower-cased owner/repo pairs linked from a README, in order, without the list's own repo."""
    out = []
    for m in LINK_RE.finditer(text):
        owner, repo = m.group(1), m.group(2).rstrip(".")
        repo = repo[:-4] if repo.endswith(".git") else repo
        full = f"{owner}/{repo}".lower()
        if owner.lower() in NOT_OWNERS or full == own.lower() or not repo or full in out:
            continue
        out.append(full)
    return out


def src_awesome(run):
    errors = []
    for lst in AWESOME:
        try:
            _awesome_one(run, lst)
        except (GhError, ValueError, KeyError, TypeError) as e:
            errors.append(f"{lst}: {e}")
    if errors:
        raise GhError("; ".join(errors))


def _awesome_one(run, lst):
    name = f"awesome:{lst.lower()}"
    cur = _cursor_json(run, name)
    sha = run.gh.api(f"repos/{lst}/commits/HEAD", "--jq", ".sha").strip()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise GhError(f"{lst}: head commit is not a sha")
    if sha == cur.get("sha"):
        return
    text = run.gh.api(f"repos/{lst}/readme?ref={sha}", *RAW)
    seen = set(cur.get("links", []))
    done, deferred = set(seen), False
    for link in readme_links(text, lst):
        if link in seen:
            continue
        if visit(run, "awesome", link, discovery=True) == DEFERRED:
            deferred = True
        else:
            done.add(link)
    # links left over by the budget stay "added": keep the old sha so the next run reads the README again
    _set_cursor(run, name, {"sha": cur.get("sha") if deferred else sha, "links": sorted(done)})


# -- e. Hacker News (Algolia) -----------------------------------------------------------------
def _hn_get(run, q, since):
    params = {"query": q, "tags": "story", "numericFilters": f"created_at_i>{since},points>={HN_MIN_POINTS}"}
    url = run.hn_url + ("&" if "?" in run.hn_url else "?") + urllib.parse.urlencode(params, safe=",")
    if urllib.parse.urlparse(url).scheme not in ("http", "https", "file"):
        raise ValueError(f"FACTORY_HN_URL must be an http(s) or file URL, got {run.hn_url!r}")
    run.hn_calls += 1
    with urllib.request.urlopen(url, timeout=HN_TIMEOUT) as r:  # noqa: S310 (scheme checked above)
        return json.loads(r.read().decode("utf-8", "replace"))


def src_hn(run):
    cur = scoutdb.get_cursor(run.con, "hn")
    since = int(cur) if cur and cur.isdigit() else int(run.now.timestamp()) - HN_FIRST_LOOKBACK_DAYS * 86400
    newest, repos = since, {}
    try:
        for q in HN_QUERIES:
            for hit in _hn_get(run, q, since).get("hits", []):
                newest = max(newest, int(hit.get("created_at_i") or 0))
                repo = _github_repo(hit.get("url") or "")
                if repo and REPO_RE.match(repo):
                    repos.setdefault(repo.lower(), repo)
    except (urllib.error.URLError, OSError, AttributeError, TypeError) as e:  # URLError is an OSError
        raise GhError(f"hacker news: {e}") from e
    for repo in repos.values():
        visit(run, "hn", repo, discovery=True)
    scoutdb.set_cursor(run.con, "hn", str(newest), run.ts)


# -- driver -------------------------------------------------------------------------------------
RUNNERS = (("marketplace", src_marketplace), ("skills", src_skills), ("search", src_search),
           ("awesome", src_awesome), ("hn", src_hn))


def fetch(con, now, gh=None, hn_url=None):
    """Run all sources in order. Returns the Run (stats, errors, call counts). Each candidate is written as it
    is processed; a failing source leaves its cursor untouched so the next run retries it."""
    run = Run(con, gh or Gh(), now, hn_url)
    memo = _cursor_json(run, "dropped")
    run.dropped = {k: v for k, v in memo.items() if isinstance(v, str)}
    for name, fn in RUNNERS:
        try:
            fn(run)
        except (GhError, scoutdb.ScoutError, sqlite3.Error, ValueError, KeyError, TypeError, AttributeError) as e:
            run.errors.append(f"{name}: {' '.join(str(e).split())[:200]}")
    for name, msgs in run.soft.items():
        run.errors.append(f"{name}: {len(msgs)} candidate(s) failed, first: {' '.join(msgs[0].split())[:160]}")
    keep = dict(list(run.dropped.items())[-DROPPED_MAX:])
    _set_cursor(run, "dropped", keep)
    return run


def summary(run):
    new = sum(s["new"] for s in run.stats.values())
    upd = sum(s["updated"] for s in run.stats.values())
    return (f"fetch: new {new}, updated {upd}, per-source {json.dumps(run.stats, sort_keys=True)}, "
            f"gh calls {run.gh.calls} (search {run.gh.search_calls}), hn calls {run.hn_calls}, "
            f"errors {json.dumps(run.errors)}")
