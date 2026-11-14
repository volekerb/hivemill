// Hivemill dashboard for the Herdr app. Two faces from one mod:
//   props {mode: "bar"}  the window's status bar (the "hivemill" layout puts it in place of workspace-status):
//                        where you are, each project and the factory in one line, and the most urgent alert.
//   otherwise            the dashboard tab: alerts, budget and cost, resources, projects' PRs, the factory.
// Agents themselves live in the app's agents list; this shows only what that list can't.
// Settings: ~/.config/hivemill/dashboard.json. Docs: docs/herdr-app-dashboard.md.

import { View, div, svg } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { props, theme, run, exec, spaces, panes, window, keys, notify, nextCommand } from "herdr";
import { data, config, tick, ago, money, dur, pipeline, alerts, usage, orgsShown, webUrl, levelColor, warn, good, demo, hidden, clean } from "./data.js";

const MOD = "hivemill";
const alpha = (c, a) => (typeof c === "string" && c.length === 7 ? c + a : c);
const pct = (v) => (v == null ? "–" : `${Math.round(Number(v))}%`);
const toneColor = (t, tone) => ({ danger: t.blocked, error: t.blocked, warning: warn(t), success: good(t), info: t.accent }[tone] ?? t.muted);

function openUrl(href) {
  const url = href?.startsWith("http") ? href : webUrl(href ?? "/");
  if (url) exec(window()?.platform === "macos" ? "open" : "xdg-open", [url]);
  else notify({ title: "Hivemill", body: "Set factory.web_url in ~/.config/hivemill/dashboard.json to open factory pages.", level: "info" });
}

// The dashboard tab: show it, or make it in the first project's space (else the shown space).
export function showDashboard() {
  const pane = panes().find((p) => p.mod === MOD);
  if (pane) {
    const sp = spaces().find((s) => (s.tabs ?? []).some((tb) => tb.id === pane.tab));
    if (sp) run("space.select", { id: sp.id });
    run("tab.select", { id: pane.tab });
    return;
  }
  const want = (config().projects ?? [])[0]?.space;
  const sp = spaces().find((s) => s.machine == null && (s.id === want || s.name === want));
  if (sp) run("space.select", { id: sp.id });
  run("tab.new", { ...(sp ? { space: sp.id } : {}), name: "hivemill", mod: MOD, props: { mode: "full" } });
}

function focusAgent(a) {
  if (a?.agent?.address) return run("agent.focus", { terminal: a.agent.address });
  showDashboard();
}

export default class Dashboard extends View {
  init(_props, cx) {
    this.me = Math.random().toString(36).slice(2);
    this.mode = props()?.mode === "bar" ? "bar" : "full";
    this.width = 0;
    try {
      this.told = JSON.parse(localStorage.getItem("told") ?? "{}");
    } catch {
      this.told = {};
    }
    tick(this.me, cx);
    if (this.mode === "bar")
      // "hivemill.show" (a clicked alert, or a key bound to it) opens the dashboard tab.
      cx.spawn(async (cx) => {
        for (;;) {
          const c = await nextCommand();
          if (c === "show") showDashboard();
        }
      });
    cx.timer.every(15_000, (cx) => {
      tick(this.me, cx);
      if (this.mode === "bar") this.tell();
    });
  }

  // The bar copy raises a notification once per new alert.
  tell() {
    const now = Date.now();
    let changed = false;
    for (const a of alerts()) {
      if (this.told[a.key]) continue;
      this.told[a.key] = now;
      changed = true;
      notify({ title: a.level === "danger" ? "Hivemill needs attention" : "Hivemill", body: a.text, level: a.level === "danger" ? "error" : "warning", action: { name: `${MOD}.show`, args: {} } });
    }
    for (const [k, v] of Object.entries(this.told))
      if (now - v > 24 * 3600_000) {
        delete this.told[k];
        changed = true;
      }
    if (changed)
      try {
        localStorage.setItem("told", JSON.stringify(this.told));
      } catch {}
  }

  render() {
    return this.mode === "bar" ? this.renderBar() : this.renderFull();
  }

  // ---- the status bar --------------------------------------------------------

  paletteHint() {
    const mac = window()?.platform === "macos";
    const mod = { cmd: mac ? "⌘" : "Ctrl+", ctrl: mac ? "⌃" : "Ctrl+", shift: mac ? "⇧" : "Shift+", alt: mac ? "⌥" : "Alt+", option: mac ? "⌥" : "Alt+" };
    const b = keys().find((k) => k.section === "global" && k.action === "app.palette");
    const parts = b && b.keys.endsWith("--") ? [...b.keys.slice(0, -2).split("-"), "-"] : b?.keys.split("-");
    return parts ? parts.map((p) => mod[p.toLowerCase()] ?? p.toUpperCase()).join("") : "";
  }

  renderBar() {
    const t = theme();
    const d = data();
    const cfg = config();
    const space = spaces().find((s) => s.active);
    const list = alerts(d);
    const top = list[0];
    const f = d.factory;
    const orgs = orgsShown(d);
    const fLive = f ? f.system?.agents?.live ?? f.live_agents ?? 0 : null;
    const fNeeds = f ? (f.needs_you ?? []).filter((n) => (!n.org || orgs.has(n.org)) && !hidden(n.subject)).length : 0;
    const u = usage(d);
    const seg = (id, onClick, ...kids) =>
      h_flex().id(id).gap(6).items_center().px(6).h_full().hover((s) => s.bg(t.hover).text_color(t.foreground)).on_click(onClick).children(kids);
    const dot = (c) => div().size(6).rounded(3).flex_none().bg(c);
    const sep = () => div().w(1).h(12).bg(t.border);
    const label = (icon, text) => h_flex().gap(6).items_center().child(svg(`mods/workspace-chrome/${icon}.svg`).size(13)).child(text);
    const stallMs = (cfg.alerts?.stall_minutes ?? 60) * 60_000;

    const bar = h_flex().id("root").size_full().px(8).gap(6).items_center().bg(t.chrome).border_t(1).border_color(t.border).text_size(11.8).text_color(t.muted)
      .child(h_flex().px(6).gap(14).items_center().child(label(space?.machine && !demo() ? "server" : "monitor", demo() ? "This computer" : space?.machine ?? "This computer"))
        .when(Boolean(space?.branch) && !demo(), (el) => el.child(label("branch", space.branch))));

    for (const p of (cfg.projects ?? []).filter((x) => x.github && !hidden(x.name ?? x.github))) {
      const pl = pipeline(p.github, d);
      bar.child(sep()).child(
        seg(`b-${p.github}`, () => showDashboard(),
          dot(pl.error ? t.blocked : pl.ready.length && pl.lastMerge && Date.now() - pl.lastMerge > stallMs ? warn(t) : good(t)),
          div().child(`${p.name ?? p.github} ${pl.loaded ? `${pl.ready.length} PRs` : "…"}`),
          div().text_color(t.dim).child(pl.lastMerge ? `merged ${ago(pl.lastMerge)} ago` : "")),
      );
    }
    bar.child(sep()).child(
      seg("b-factory", () => showDashboard(),
        dot(d.factoryError ? t.blocked : good(t)),
        div().child(d.factoryError && !f ? "Factory offline" : f ? `Factory ${fLive} live` : "Factory…"),
        div().text_color(fNeeds ? warn(t) : t.dim).child(f && fNeeds ? `${fNeeds} need you` : ""),
        div().text_color(u.seven >= (cfg.alerts?.usage_danger_pct ?? 90) ? t.blocked : t.dim).child(orgs.has("claude") && u.seven != null ? `wk ${pct(u.seven)}` : ""),
        div().text_color(t.dim).child(d.through?.cost?.cost_median != null ? `${money(d.through.cost.cost_median)}/PR` : ""),
        div().text_color(t.dim).child(d.tokens?.byDay?.[0]?.cost != null ? `${money(d.tokens.byDay[0].cost)} today` : "")),
    );
    bar.child(div().flex_1());
    if (top)
      bar.child(
        seg("b-alert", () => (top.agent ? focusAgent(top) : showDashboard()),
          div().text_color(levelColor(t, top.level)).child("●"),
          div().max_w(420).truncate().text_color(levelColor(t, top.level)).child(top.text),
          div().text_color(t.dim).child(list.length > 1 ? `+${list.length - 1}` : "")),
      );
    const hint = this.paletteHint();
    return bar.child(div().id("palette").px(6).text_color(t.dim).hover((s) => s.text_color(t.foreground)).on_click(() => run("app.palette")).child(hint ? `${hint}  commands` : "commands"));
  }

  // ---- building blocks -----------------------------------------------------------

  card(t, title, right, body) {
    return v_flex().flex_none().rounded(10).border(1).border_color(t.border).bg(t.surface).overflow_hidden()
      .child(h_flex().h(34).px(12).gap(8).items_center().bg(t.chrome).border_b(1).border_color(t.border)
        .child(div().flex_1().truncate().text_size(12.5).font_semibold().text_color(t.foreground).child(title))
        .when(Boolean(right), (el) => el.child(right)))
      .child(body);
  }

  stat(t, label, value, color) {
    return v_flex().flex_1().min_w(0).px(10).py(8).rounded(8).bg(t.raised)
      .child(div().truncate().text_size(16).font_semibold().text_color(color ?? t.foreground).child(String(value)))
      .child(div().truncate().text_size(10.5).text_color(t.dim).child(label));
  }

  sub(t, text, extra) {
    return h_flex().px(12).pt(12).pb(4).gap(6).items_center().text_size(10.5).text_color(t.dim)
      .child(div().child(text.toUpperCase()))
      .when(extra != null, (el) => el.child(div().text_color(t.muted).child(String(extra))));
  }

  row(t, id, onClick, kids) {
    return h_flex().id(id).mx(6).px(6).py(4).gap(8).rounded(6).items_center().hover((s) => s.bg(t.hover)).on_click(onClick).children(kids);
  }

  btn(t, id, text, onClick) {
    return div().id(id).px(8).py(2).rounded(5).text_size(11).text_color(t.muted).border(1).border_color(t.border)
      .hover((s) => s.bg(t.raised).text_color(t.foreground))
      .on_click((_e, cx) => onClick(cx))
      .child(text);
  }

  meter(t, id, label, value, frac, limitText) {
    const f = Math.max(0, Math.min(1, frac ?? 0));
    const c = f >= 0.95 ? t.blocked : f >= 0.8 ? warn(t) : good(t);
    return v_flex().id(id).px(12).py(5).gap(4)
      .child(h_flex().gap(6).items_center()
        .child(div().flex_1().truncate().text_size(12).text_color(t.foreground).child(label))
        .child(div().flex_none().text_size(12).font_semibold().text_color(frac == null ? t.dim : c).child(value))
        .when(Boolean(limitText), (el) => el.child(div().flex_none().text_size(10.5).text_color(t.dim).child(limitText))))
      .child(div().h(5).w_full().rounded(3).bg(t.border).child(div().h_full().w(`${(f * 100).toFixed(1)}%`).rounded(3).bg(c)));
  }

  bars(t, id, items, valueOf, labelOf, color) {
    const max = Math.max(1, ...items.map(valueOf));
    return h_flex().id(id).px(12).pt(6).h(70).gap(3).items_end().children(
      items.map((x, i) =>
        v_flex().flex_1().min_w(0).items_center().gap(2).justify_end().h_full()
          .child(div().w_full().rounded(2).bg(color).h(Math.max(2, Math.round((valueOf(x) / max) * 46))))
          .child(div().text_size(9).text_color(t.dim).child(labelOf(x, i))),
      ),
    );
  }

  // ---- cards -----------------------------------------------------------------------

  alertsCard(t, d) {
    const list = alerts(d);
    const body = v_flex().py(6);
    if (!list.length) body.child(div().px(12).py(6).text_size(12).text_color(t.dim).child("All clear: nothing is waiting on a person."));
    body.children(
      list.map((a, i) =>
        this.row(t, `al-${i}`, () => (a.agent ? focusAgent(a) : null), [
          div().size(8).rounded(4).flex_none().bg(levelColor(t, a.level)),
          div().flex_1().min_w(0).truncate().text_size(12.5).text_color(t.foreground).child(a.text),
          div().flex_none().text_size(10.5).text_color(t.dim).child(a.agent ? "show ›" : ""),
        ]),
      ),
    );
    return this.card(t, "Alerts", div().text_size(10.5).text_color(t.dim).child(d.scanAt ? (ago(d.scanAt) === "now" ? "checked just now" : `checked ${ago(d.scanAt)} ago`) : "checking…"), body);
  }

  projectCard(t, d, p) {
    const pl = pipeline(p.github, d);
    const body = v_flex().pb(10);
    body.child(h_flex().px(10).pt(10).gap(6).children([
      this.stat(t, "ready PRs", pl.loaded ? pl.ready.length : "–"),
      this.stat(t, "drafts", pl.loaded ? pl.drafts : "–", t.muted),
      this.stat(t, "last merge", pl.lastMerge ? ago(pl.lastMerge) : "–", pl.lastMerge && Date.now() - pl.lastMerge > 3600_000 ? warn(t) : good(t)),
    ]));
    if (pl.error) body.child(div().px(12).pt(8).text_size(11).text_color(t.blocked).child(`gh: ${pl.error}`));
    const waiting = [...pl.ready].sort((a, b) => a.updated - b.updated);
    body.child(this.sub(t, "Open PRs, longest waiting first", waiting.length));
    body.children(waiting.map((x) =>
      this.row(t, `pr-${p.github}-${x.number}`, () => openUrl(x.url), [
        div().flex_none().w(demo() ? 0 : 44).text_size(11).text_color(t.dim).child(demo() ? "" : `#${x.number}`),
        div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(x.title)),
        div().flex_none().text_size(10.5).text_color(Date.now() - x.updated > 3 * 3600_000 ? warn(t) : t.dim).child(ago(x.updated)),
      ])));
    const merged = pl.merged.slice(0, 4);
    if (merged.length) {
      body.child(this.sub(t, "Recently merged", null));
      body.children(merged.map((m) =>
        this.row(t, `mg-${p.github}-${m.number}`, () => openUrl(m.url), [
          div().flex_none().text_size(11).text_color(good(t)).child("✓"),
          div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(m.title)),
          div().flex_none().text_size(10.5).text_color(t.dim).child(ago(m.merged)),
        ])));
    }
    return this.card(t, p.name ?? p.github, this.btn(t, `gh-${p.github}`, "GitHub ↗", () => openUrl(`https://github.com/${p.github}/pulls`)), body);
  }

  budgetCard(t, d) {
    const orgs = orgsShown(d);
    const body = v_flex().pb(10);
    if (orgs.has("claude")) {
      const u = usage(d);
      body.child(this.sub(t, "Claude budget", u.resets ? `resets ${new Date(u.resets * 1000).toLocaleDateString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit" })}` : null));
      body.child(this.meter(t, "m-7d", "Weekly (7-day)", pct(u.seven), u.seven != null ? u.seven / u.capSeven : null, `cap ${u.capSeven}%`));
      body.child(this.meter(t, "m-5h", "5-hour window", pct(u.five), u.five != null ? u.five / u.capFive : null, `cap ${u.capFive}%`));
    }
    const cur = d.res?.cursor ?? d.factory?.system?.cursor;
    if (orgs.has("cursor") && cur) {
      const bad = (cur.models ?? []).filter((m) => m.status !== "ok");
      body.child(this.sub(t, "Cursor", cur.tier ?? null));
      body.child(h_flex().px(12).py(5).gap(6).items_center()
        .child(div().flex_1().text_size(12).text_color(t.foreground).child((cur.models ?? []).map((m) => m.model).join(", ") || "models"))
        .child(div().text_size(12).text_color(bad.length ? t.blocked : good(t)).child(bad.length ? `${bad.map((m) => m.model).join(", ")} limited` : "ok"))
        .child(div().text_size(10.5).text_color(t.dim).child(cur.hours != null ? `${Math.round(cur.hours)} agent-hours` : "")));
    }
    const tk = d.tokens, th = d.through;
    if (tk || th) {
      const byOrg = (tk?.byOrg ?? []).filter((o) => orgs.has(o.key));
      const total = byOrg.length ? byOrg.reduce((s, o) => s + (o.cost ?? 0), 0) : tk?.total;
      // The factory reports per-day spend, cost per PR and per-task cost for all orgs together.
      if ((tk?.byOrg ?? []).some((o) => !orgs.has(o.key) && o.cost))
        body.child(div().px(12).pt(8).text_size(10.5).text_color(t.dim).child("Today, cost per PR, spend per day and tasks below cover every org."));
      body.child(this.sub(t, `Spend · last ${tk?.period ?? 7} days`, total != null ? money(total) : null));
      body.child(h_flex().px(10).pt(4).gap(6).children([
        this.stat(t, "today", money((tk?.byDay ?? [])[0]?.cost)),
        ...(byOrg.length > 1 ? byOrg.map((o) => this.stat(t, o.key === "cursor" ? "Cursor (est.)" : "Claude", money(o.cost))) : []),
      ]));
      const c = th?.cost ?? {};
      body.child(this.sub(t, "Cost per merged PR", c.merged ? `${c.priced}/${c.merged} priced · ${th.days}d` : null));
      body.child(h_flex().px(10).pt(4).gap(6).children([
        this.stat(t, "median", money(c.cost_median)),
        this.stat(t, "p90", money(c.cost_p90), c.cost_p90 > 10 ? warn(t) : null),
        this.stat(t, "average", money(c.figure)),
        this.stat(t, "time to merge", th?.median_s != null ? dur(th.median_s) : "–", t.muted),
      ]));
      if (c.tokens_median)
        body.child(div().px(12).pt(4).text_size(10.5).text_color(t.dim).child(
          `${(c.tokens_median / 1e6).toFixed(1)}M tokens median · ${(c.tokens_p90 / 1e6).toFixed(1)}M p90${orgs.has("cursor") && c.cursor_tasks ? ` · ${c.cursor_tasks} Cursor tasks unpriced` : ""}`));
      const days = [...(tk?.byDay ?? [])].slice(0, 7).reverse();
      if (days.length) {
        body.child(this.sub(t, "Spend per day", null));
        body.child(this.bars(t, "spend-days", days, (x) => x.cost ?? 0, (x) => money(x.cost).replace(".00", ""), t.accent));
      }
      const tasks = (tk?.byTask ?? []).filter((x) => !hidden(x.title));
      if (tasks.length) {
        body.child(this.sub(t, "Most expensive tasks", null));
        body.children(tasks.map((x) =>
          this.row(t, `cost-${x.key}`, () => openUrl(`/task/${x.key}`), [
            div().flex_none().w(demo() ? 0 : 52).text_size(10.5).text_color(t.dim).child(demo() ? "" : x.key),
            div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(x.title ?? "")),
            div().flex_none().text_size(11.5).font_semibold().text_color(x.cost > 10 ? warn(t) : t.muted).child(money(x.cost)),
          ])));
      }
    }
    return this.card(t, "Budget & cost", this.btn(t, "tok-open", "Tokens ↗", () => openUrl("/tokens")), body);
  }

  resourcesCard(t, d) {
    const r = d.res;
    const body = v_flex().pb(10);
    if (!r) {
      body.child(div().px(12).py(8).text_size(12).text_color(t.dim).child(d.factoryError ? "Factory unreachable" : "Loading…"));
      return this.card(t, "Resources", null, body);
    }
    const m = r.machine ?? {};
    const gb = (mb) => (mb / 1024).toFixed(1);
    body.child(this.sub(t, "Factory machine", m.serve_uptime_s ? `up ${dur(m.serve_uptime_s)}` : null));
    body.child(this.meter(t, "r-mem", "Memory", m.mem_total_mb ? `${gb(m.mem_total_mb - m.mem_available_mb)} / ${gb(m.mem_total_mb)} GB` : "–", m.mem_total_mb ? 1 - m.mem_available_mb / m.mem_total_mb : null, m.mem_available_mb != null ? `${gb(m.mem_available_mb)} GB free` : null));
    if (m.swap_total_mb && !hidden("swap")) body.child(this.meter(t, "r-swap", "Swap", `${gb(m.swap_used_mb)} / ${gb(m.swap_total_mb)} GB`, m.swap_used_mb / m.swap_total_mb));
    const load = Array.isArray(m.load) ? m.load : [];
    body.child(this.meter(t, "r-load", `Load (${m.cpus ?? "?"} CPUs)`, load.length ? load.map((x) => x.toFixed(1)).join(" · ") : "–", load.length && m.cpus ? load[0] / m.cpus : null, "1 · 5 · 15 min"));
    body.child(this.sub(t, "Spawn gate", null));
    body.children((r.rows ?? []).map((row, i) =>
      h_flex().id(`gate-${i}`).px(12).py(3).gap(8).items_center()
        .child(div().size(7).rounded(4).flex_none().bg(row.state === "ok" ? good(t) : row.state === "warn" ? warn(t) : t.blocked))
        .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(row.label))
        .child(div().flex_none().text_size(12).text_color(t.muted).child(row.value))
        .child(div().flex_none().w(110).truncate().text_size(10.5).text_color(t.dim).child(row.limit && row.limit !== "-" ? row.limit : ""))));
    const per = (d.through?.perDay ?? []).slice(-14);
    if (per.length) {
      body.child(this.sub(t, "Merges per day", d.through?.merges != null ? `${d.through.merges} in ${d.through.days}d` : null));
      body.child(this.bars(t, "merge-days", per, (x) => x.merges ?? 0, (x) => String(x.merges ?? 0), good(t)));
    }
    return this.card(t, "Resources", this.btn(t, "res-open", "Resources ↗", () => openUrl("/resources")), body);
  }

  factoryCard(t, d) {
    const f = d.factory;
    const orgs = orgsShown(d);
    const inOrg = (x) => !x.org || orgs.has(x.org);
    const right = h_flex().gap(6).items_center()
      .child(div().text_size(10.5).text_color(d.factoryError ? t.blocked : t.dim).child(d.factoryError ? (f ? `offline · ${ago(d.factoryAt)} old` : "offline") : f ? ago(d.factoryAt) : "loading…"))
      .child(this.btn(t, "f-open", "Open ↗", () => openUrl("/")));
    const body = v_flex().pb(10);
    if (!f) {
      body.child(v_flex().m(10).p(12).gap(4).rounded(8).border(1).border_color(d.factoryError ? alpha(t.blocked, "66") : t.border)
        .child(div().text_size(12.5).text_color(d.factoryError ? t.blocked : t.muted).child(d.factoryError ? "Factory unreachable" : "Reaching the factory…"))
        .when(Boolean(d.factoryError), (el) => el.child(div().text_size(11).text_color(t.dim).child(`${d.factoryError}. Check factory.url / factory.machine in ~/.config/hivemill/dashboard.json.`))));
      return this.card(t, "Factory", right, body);
    }
    const sa = f.system?.agents ?? {};
    const needs = (f.needs_you ?? []).filter((n) => inOrg(n) && !hidden(n.subject));
    body.child(h_flex().px(10).pt(10).gap(6).children([
      this.stat(t, "live / cap", `${sa.live ?? f.live_agents ?? 0}/${sa.cap ?? "?"}`, good(t)),
      this.stat(t, "queued", sa.queued ?? f.queued_agents ?? 0),
      this.stat(t, "open PRs", f.open_prs ?? 0),
      this.stat(t, "needs you", needs.length, needs.length ? warn(t) : null),
    ]));
    const lanes = (f.lanes ?? []).filter((l) => orgs.has(l.org));
    if (lanes.length > 1 || (lanes.length === 1 && orgs.size > 1)) {
      body.child(this.sub(t, "Lanes", null));
      body.children(lanes.map((l) =>
        this.row(t, `lane-${l.org}`, () => openUrl(`/?org=${l.org}`), [
          div().size(7).rounded(4).flex_none().bg(toneColor(t, l.health?.tone)),
          div().flex_none().w(60).text_size(12).text_color(t.foreground).child(l.label ?? l.org),
          div().flex_1().min_w(0).truncate().text_size(11).text_color(t.muted).child(`${l.workers_live ?? 0} workers · ${l.leads_awake ?? 0} leads awake · ${l.goals_active ?? 0} goals`),
          div().flex_none().text_size(10.5).text_color(toneColor(t, l.health?.tone)).child(demo() ? ((n) => (n ? `${n} need${n === 1 ? "s" : ""} you` : "ok"))(needs.filter((x) => x.org === l.org).length) : l.health?.text ?? ""),
        ])));
    }
    body.child(this.sub(t, "Needs you", needs.length));
    if (!needs.length) body.child(div().px(12).py(2).text_size(11.5).text_color(t.dim).child("Nothing waiting on you."));
    body.children(needs.slice(0, 8).map((n, i) =>
      this.row(t, `need-${i}`, () => openUrl(n.href), [
        div().size(7).rounded(4).flex_none().bg(toneColor(t, n.tone)),
        v_flex().flex_1().min_w(0)
          .child(div().truncate().text_size(12).text_color(t.foreground).child(clean(n.subject ?? n.ref ?? "?")))
          .child(div().truncate().text_size(10.5).text_color(t.dim).child([orgs.size > 1 ? n.org : null, n.kind, demo() ? null : n.ref, n.age].filter(Boolean).join(" · "))),
      ])));
    if (needs.length > 8) body.child(this.row(t, "need-more", () => openUrl("/"), [div().text_size(11).text_color(t.accent).child(`+${needs.length - 8} more in the browser`)]));

    const goals = (f.goals ?? []).filter((g) => g.status === "active" && inOrg(g) && !hidden(g.title))
      .sort((a, b) => (a.priority ?? 9) - (b.priority ?? 9) || (b.last_activity ?? "").localeCompare(a.last_activity ?? ""));
    if (goals.length) {
      body.child(this.sub(t, "Active goals", `${goals.length} · ${f.goals_by_status?.done ?? 0} done`));
      body.children(goals.map((g) => {
        const done = g.tasks_done ?? 0, total = g.tasks_total ?? 0, frac = total ? Math.min(1, done / total) : 0;
        return v_flex().id(`goal-${g.id}`).mx(6).px(6).py(4).gap(3).rounded(6).hover((s) => s.bg(t.hover)).on_click(() => openUrl(`/goal/${g.id}`))
          .child(h_flex().gap(6).items_center()
            .child(div().flex_none().text_size(10.5).text_color(t.dim).child(demo() ? "" : g.id))
            .child(div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(g.title ?? "")))
            .child(div().flex_none().text_size(10.5).text_color(t.muted).child(`${done}/${total}`)))
          .child(div().h(4).w_full().rounded(2).bg(t.border).child(div().h_full().w(`${(frac * 100).toFixed(1)}%`).rounded(2).bg(done >= total && total ? good(t) : t.accent)));
      }));
    }
    // Factory agents run on the factory's own terminals, so this app's agents list may not show them.
    const rows = (f.agents?.rows ?? []).filter((a) => (a.status === "live" || a.status === "running") && inOrg(a) && !hidden(a.task_title));
    if (rows.length) {
      body.child(this.sub(t, "Factory agents", `${rows.length} · ${f.agents?.queued ?? 0} queued`));
      body.children(rows.map((a, i) =>
        this.row(t, `fa-${i}`, () => openUrl("/agents"), [
          div().size(7).rounded(4).flex_none().bg(good(t)),
          v_flex().flex_1().min_w(0)
            .child(h_flex().gap(6).child(div().truncate().text_size(12).text_color(t.foreground).child(clean(a.name))).child(div().flex_none().text_size(10.5).text_color(t.dim).child(a.role ?? "")))
            .when(Boolean(a.task_title || a.task_id), (el) => el.child(div().truncate().text_size(10.5).text_color(t.muted).child(demo() ? clean(a.task_title ?? "") : [a.task_id, a.task_title].filter(Boolean).join(" ")))),
          div().flex_none().text_size(10.5).text_color(t.dim).child(ago(Date.parse(a.last_event))),
        ])));
    }
    const shipped = (f.shipped?.recent ?? []).filter((s) => !hidden(s.title)).slice(0, 5);
    if (shipped.length) {
      body.child(this.sub(t, "Shipped", f.quality?.first_try_pct != null ? `${f.quality.first_try_pct}% first try` : null));
      body.children(shipped.map((s, i) =>
        this.row(t, `ship-${i}`, () => openUrl(s.pr_url || `/task/${s.task_id}`), [
          div().flex_none().text_size(11).text_color(good(t)).child("✓"),
          div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(s.title ?? s.task_id)),
          div().flex_none().text_size(10.5).text_color(t.dim).child(ago(Date.parse(s.merged))),
        ])));
    }
    return this.card(t, "Factory", right, body);
  }

  renderFull() {
    const t = theme();
    const d = data();
    const cols = this.width >= 1300 ? 3 : this.width >= 860 ? 2 : 1;
    const col = (cards) => v_flex().flex_none().flex_1().min_w(0).gap(12).children(cards);
    const projects = (config().projects ?? []).filter((p) => p.github && !hidden(p.name ?? p.github)).map((p) => this.projectCard(t, d, p));
    const alertsC = this.alertsCard(t, d), budget = this.budgetCard(t, d), res = this.resourcesCard(t, d), factory = this.factoryCard(t, d);
    const grid =
      cols === 3
        ? projects.length
          ? h_flex().flex_none().gap(12).items_start().child(col([alertsC, ...projects])).child(col([budget, res])).child(col([factory]))
          : h_flex().flex_none().gap(12).items_start().child(col([alertsC, res])).child(col([budget])).child(col([factory]))
      : cols === 2 ? h_flex().flex_none().gap(12).items_start().child(col([alertsC, budget, ...projects])).child(col([res, factory]))
      : v_flex().flex_none().gap(12).children([alertsC, budget, ...projects, res, factory]);
    const header = h_flex().gap(8).items_center().px(4)
      .child(div().flex_1().text_size(15).font_semibold().text_color(t.foreground).child("Hivemill"))
      .child(this.btn(t, "refresh", "Refresh", (cx) => tick(this.me, cx, true)));
    return v_flex().id("root").size_full().bg(t.background)
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - this.width) > 20) {
          this.width = size.width;
          cx.notify();
        }
      })
      .child(v_flex().id("scroll").flex_1().min_h(0).overflow_y_scrollbar()
        .child(v_flex().flex_none().w_full().p(14).gap(12).child(header).child(grid)));
  }
}
