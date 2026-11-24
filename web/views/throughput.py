"""Flow page (G-069, replaces Throughput; /throughput redirects here): where PRs wait.

Every figure comes from `hm-obs api flow` (ctx.obs), which reads the pr-flow stage hours the tick stores in
state/obs.db. The page shows a strip (merged, median and p90 open->merge, PRs needing 2+ review rounds, open over 6 h),
a stage map of the main line coder -> CI wait -> review+QA -> judge -> merge with the rework loop and the side queues
(hold, capacity gate, red main), each with its hours and share of all stage hours, and the PRs stuck now.
"""
from web import charts, queries
from web.components import banners, data_table, empty_state, howto, section, strip
from web.html import el, link

TITLE = "Flow"
DOCS = "docs/obs.md"
REPO_URL = "https://github.com/{repo}/pull/{number}"


def _query(ctx):
    q = getattr(ctx, "query", None)
    return q if isinstance(q, dict) else {}


def data(ctx, **params):
    rng = queries.range_param(_query(ctx))
    obs, err = ctx.obs("flow") if getattr(ctx, "obs", None) else (None, "hm-obs is not available in this server")
    return {"range": rng[0], "range_label": rng[2], "obs": obs, "error": err}


# ───────────────────────────── rendering ─────────────────────────────

def _h(v):
    if v is None:
        return "–"
    return f"{v:.1f} h" if round(v, 1) < 10 else f"{v:,.0f} h"


def _pct(v):
    return "–" if v is None else f"{v:.0f}%"


def _strip(d):
    o = d["obs"]
    multi_pct = o.get("prs_2plus_rounds_pct") if o.get("prs") else None
    return strip([
        {"label": "Merged", "value": o.get("merged"), "delta": d["range_label"],
         "definition": "PRs merged in the window."},
        {"label": "Median open → merge", "value": _h(o.get("open_to_merge_median_h")),
         "definition": "Median hours from PR open to merge, over the PRs merged in the window."},
        {"label": "p90 open → merge", "value": _h(o.get("open_to_merge_p90_h")),
         "definition": "90th percentile of the same hours: the slow tail."},
        {"label": "2+ review rounds", "value": _pct(multi_pct),
         "delta": f"{o.get('prs_2plus_rounds', 0)} of {o.get('prs', 0)}" if o.get("prs") else None,
         "tone": "warn" if multi_pct is not None and multi_pct > 30 else "neutral",
         "definition": "PRs in the window that went back to the coder at least once after review or QA."},
        {"label": "Open > 6 h", "value": o.get("open_over_6h"), "tone": "warn" if o.get("open_over_6h") else "neutral",
         "delta": "see Stuck now" if o.get("open_over_6h") else None,
         "definition": "Open PRs whose age is over 6 hours right now."},
    ], key="flow-strip")


def _stage(s):
    if s is None:
        return None
    share = s.get("share_pct") or 0
    w = round(min(100.0, max(0.0, float(share))), 1)   # an SVG rect, not inline style: the CSP forbids style attributes
    bar = charts._svg([el("rect", class_="stage-fill", x=0, y=0, width=w, height=8)], "0 0 100 8", preserveAspectRatio="none",
                      role="img", focusable="false", **{"class": "stage-bar", "aria-label": f"{share:.0f}% of stage hours"})
    return el("li", el("span", s["label"], class_="stage-name"), bar,
              el("span", _h(s.get("hours")), class_="stage-h"),
              el("span", _pct(share), class_="stage-share"),
              class_=f"stage lane-{s.get('lane') or 'other'}")


def _endpoint(label):
    return el("li", el("span", label, class_="stage-name"), el("span", "–", class_="stage-h"), class_="stage lane-main")


def _stage_map(d):
    o = d["obs"]
    by = {s["stage"]: s for s in o.get("stages") or []}
    lanes = {}
    for s in o.get("stages") or []:
        lanes.setdefault(s.get("lane") or "other", []).append(s)
    main = [_endpoint("Coder")] + [_stage(s) for s in lanes.get("main", [])] + [_endpoint("Merge")]
    parts = [el("p", "Main line", class_="stage-lane"), el("ol", main, class_="stages")]
    for lane, title in (("rework", "Rework loop"), ("side", "Side queues"), ("other", "Other")):
        if lanes.get(lane):
            parts += [el("p", title, class_="stage-lane"), el("ul", [_stage(s) for s in lanes[lane]], class_="stages")]
    if not by or not o.get("stage_hours"):
        parts.append(empty_state("No stage hours in this range"))
    total = o.get("stage_hours")
    return section("Where PRs wait", parts,
                   el("p", f"{_h(total)} of stage time in the window. Share is each stage's part of all stage hours; "
                           "Coder and Merge are the ends of the line and hold no queue time.", class_="muted"))


def _stuck(d):
    rows = d["obs"].get("stuck") or []
    labels = {s["stage"]: s["label"] for s in d["obs"].get("stages") or []}
    if not rows:
        return section("Stuck now", empty_state("No open PR is older than 6 hours"))
    cols = [{"key": "pr", "label": "PR", "cls": "c-title"}, {"key": "task", "label": "Task"},
            {"key": "stage", "label": "Stage"}, {"key": "age", "label": "Age", "cls": "num", "phone_label": True}]
    body = []
    for r in rows:
        pr = f"{r.get('repo', '')}#{r.get('number')}"
        url = REPO_URL.format(repo=r.get("repo", ""), number=r.get("number")) if r.get("repo") else None
        body.append([el("a", pr, href=url, rel="noopener", class_="mono") if url else pr,
                     link(f"/task/{r['task']}", r["task"]) if r.get("task") else "–",
                     labels.get(r.get("stage"), r.get("stage") or "–"), _h(r.get("age_h"))])
    return section("Stuck now", data_table(cols, [{"key": "all", "label": None, "count": len(body), "rows": body}], key="flow-stuck"))


def render(ctx, d):
    if d.get("obs") is None:
        return el("div", banners([f"! Flow data unavailable: {d.get('error') or 'unknown error'}"]),
                  empty_state("Flow appears once the tick has run hm-obs ingest."), howto(DOCS))
    o = d["obs"]
    warn = [el("p", w, class_="muted") for w in o.get("warnings") or []]
    return el("div", banners(o.get("banners")), _strip(d), _stage_map(d), _stuck(d), warn or None,
              el("p", f"Data as of {o.get('at') or '–'}.", class_="muted"), howto(DOCS))
