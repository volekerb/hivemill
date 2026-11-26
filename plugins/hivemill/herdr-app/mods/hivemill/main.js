// Hivemill dashboard for the Herdr app. Two faces from one mod:
//   props {mode: "bar"}  the window's status bar (the "hivemill" layout puts it in place of workspace-status):
//                        where you are, each factory in one line, and the most urgent alert.
//   otherwise            the dashboard tab: what needs you, one card per factory, budget and cost, machines.
// A factory is one hivemill runner; its numbers come from its read-only obs API (`hivemill obs`).
// Agents themselves live in the app's agents list; this shows only what that list can't.
// Settings: ~/.config/hivemill/dashboard.json. Docs: docs/herdr-app-dashboard.md.

import { View, div, svg } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { props, theme, run, exec, spaces, panes, window, keys, notify, nextCommand } from "herdr";
import { ROLE, data, oneRead, config, tick, ago, money, dur, alerts, usage, webUrl, taskUrl, levelColor, warn, good, demo, hidden, clean, machineRows, factoryViews, barSegments, spendToday, workerTerminal, stuckLimit, locationLabel, resolveSpace } from "./data.js";

const MOD = "hivemill";
const alpha = (c, a) => (typeof c === "string" && c.length === 7 ? c + a : c);
const pct = (v) => (v == null ? "–" : `${Math.round(Number(v))}%`);

function openUrl(url) {
  if (url) exec(window()?.platform === "macos" ? "open" : "xdg-open", [url]);
  else notify({ title: "Hivemill", body: "Set web_url on the factory in ~/.config/hivemill/dashboard.json to open its pages.", level: "info" });
}

// The dashboard tab: show it, or make it in the shown space.
export function showDashboard() {
  const pane = panes().find((p) => p.mod === MOD);
  if (pane) {
    const sp = spaces().find((s) => (s.tabs ?? []).some((tb) => tb.id === pane.tab));
    if (sp) run("space.select", { id: sp.id });
    run("tab.select", { id: pane.tab });
    return;
  }
  run("tab.new", { name: "hivemill", mod: MOD, props: { mode: "full" } });
}

// A factory's planner lives in the Herdr space its settings name.
const plannerSpace = (v) => resolveSpace(v.factory.space, spaces());
function showPlanner(v) {
  const sp = plannerSpace(v);
  if (sp) run("space.select", { id: sp.id });
  else openUrl(webUrl("/", v.factory));
}

// A task: its session's terminal when one is open here, else its trace page in the browser.
function openTask(v, row) {
  const term = workerTerminal(row.task);
  if (term) return run("agent.focus", { terminal: term });
  openUrl(taskUrl(v.factory, row.key));
}

// Where an alert goes: the agent, the page that shows it, or the dashboard.
function follow(a) {
  if (a?.agent?.agent?.address) return run("agent.focus", { terminal: a.agent.agent.address });
  if (a?.agent?.address) return run("agent.focus", { terminal: a.agent.address });
  if (a?.href) return openUrl(a.href);
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
      if (a.level === "info") continue; // shown on the dashboard, not worth a notification
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
    return oneRead(() => (this.mode === "bar" ? this.renderBar() : this.renderFull()));
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
    const u = usage(d);
    const today = spendToday(d);
    const seg = (id, onClick, ...kids) =>
      h_flex().id(id).gap(6).items_center().px(6).h_full().hover((s) => s.bg(t.hover).text_color(t.foreground)).on_click(onClick).children(kids);
    const dot = (c) => div().size(6).rounded(3).flex_none().bg(c);
    const sep = () => div().w(1).h(12).bg(t.border);
    const label = (icon, text) => h_flex().gap(6).items_center().child(svg(`mods/workspace-chrome/${icon}.svg`).size(13)).child(text);

    const bar = h_flex().id("root").size_full().px(8).gap(6).items_center().bg(t.chrome).border_t(1).border_color(t.border).text_size(11.8).text_color(t.muted)
      .child(h_flex().px(6).gap(14).items_center().child(label(space?.machine && !demo() ? "server" : "monitor", locationLabel(space, d)))
        .when(Boolean(space?.branch) && !demo(), (el) => el.child(label("branch", space.branch))));

    // One segment per factory: "<label> 11 working · 14 waiting", "<label> unreachable" or "<label>…".
    for (const s of barSegments(d))
      bar.child(sep()).child(
        seg(`b-${s.key}`, () => showDashboard(),
          dot(s.state === "down" ? t.blocked : s.state === "live" ? good(t) : s.state === "stale" ? warn(t) : t.dim),
          div().child(s.text),
          div().text_color(s.needs ? warn(t) : t.dim).child(s.needs ? `${s.needs} need you` : "")),
      );
    if (u.seven != null || today != null)
      bar.child(sep()).child(
        seg("b-cost", () => showDashboard(),
          div().text_color(!u.stale && u.seven >= (cfg.alerts?.usage_danger_pct ?? 90) ? t.blocked : t.dim).child(u.seven != null ? `wk ${pct(u.seven)}${u.stale ? " (old)" : ""}` : ""),
          div().text_color(t.dim).child(today != null ? `${money(today)} today` : "")),
      );
    bar.child(div().flex_1());
    if (top)
      bar.child(
        seg("b-alert", () => follow(top),
          div().text_color(levelColor(t, top.level)).child("●"),
          div().max_w(420).truncate().text_color(levelColor(t, top.level)).child(top.text),
          div().text_color(t.dim).child(list.length > 1 ? `+${list.length - 1}` : "")),
      );
    const hint = this.paletteHint();
    return bar.child(div().id("palette").px(6).text_color(t.dim).hover((s) => s.text_color(t.foreground)).on_click(() => run("app.palette")).child(hint ? `${hint}  commands` : "commands"));
  }

  // ---- building blocks -----------------------------------------------------------

  card(t, title, right, body, tag) {
    return v_flex().flex_none().rounded(10).border(1).border_color(t.border).bg(t.surface).overflow_hidden()
      .child(h_flex().h(34).px(12).gap(8).items_center().bg(t.chrome).border_b(1).border_color(t.border)
        .child(div().flex_none().max_w(300).truncate().text_size(12.5).font_semibold().text_color(t.foreground).child(title))
        .when(Boolean(tag), (el) => el.child(div().flex_none().px(6).rounded(4).bg(t.raised).text_size(10).text_color(t.muted).child(tag)))
        .child(div().flex_1())
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

  // What needs a person, over every factory and this computer. Each line says where it came from.
  needsCard(t, d) {
    const list = alerts(d);
    const body = v_flex().py(6);
    if (!list.length) body.child(div().px(12).py(6).text_size(12).text_color(t.dim).child("Nothing needs you."));
    body.children(
      list.map((a, i) =>
        this.row(t, `al-${i}`, () => follow(a), [
          div().size(8).rounded(4).flex_none().bg(levelColor(t, a.level)),
          div().flex_1().min_w(0).truncate().text_size(12.5).text_color(t.foreground).child(a.text),
          div().flex_none().text_size(10.5).text_color(t.dim).child(a.agent ? "show ›" : a.href ? "open ↗" : ""),
        ]),
      ),
    );
    const heard = factoryViews(d).map((v) => v.at).filter(Boolean);
    const at = heard.length ? Math.min(...heard) : null; // the oldest answer: the list is no fresher than that
    return this.card(t, "Needs you", div().text_size(10.5).text_color(t.dim).child(at ? (ago(at) === "now" ? "updated just now" : `updated ${ago(at)} ago`) : "loading…"), body);
  }

  budgetCard(t, d) {
    const body = v_flex().pb(10);
    const u = usage(d);
    const A = config().alerts ?? {};
    if (u.seven != null || u.five != null) {
      const when = u.resets ? `resets ${new Date(u.resets * 1000).toLocaleDateString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit" })}` : null;
      body.child(this.sub(t, "Claude budget", u.stale ? `read ${ago(u.at)} ago` : when));
      body.child(this.meter(t, "m-7d", "Weekly (7-day)", pct(u.seven), u.seven != null ? u.seven / 100 : null, `alert at ${A.usage_warn_pct ?? 80}%`));
      body.child(this.meter(t, "m-5h", "5-hour window", pct(u.five), u.five != null ? u.five / 100 : null, null));
    } else body.child(div().px(12).pt(10).text_size(11.5).text_color(t.dim).child("Claude usage: set usage_file in dashboard.json."));
    const views = factoryViews(d).filter((v) => v.loaded);
    const multi = views.length > 1;
    if (multi) {
      const sum = (k) => views.reduce((s, v) => s + (v.spend[k] ?? 0), 0);
      body.child(this.sub(t, "Spend · all factories", null));
      body.child(h_flex().px(10).pt(4).gap(6).children([this.stat(t, "today", money(sum("today"))), this.stat(t, "7 days", money(sum("week")))]));
    }
    for (const v of views) {
      const lab = multi ? ` · ${v.label}` : "";
      const s = v.spend;
      body.child(this.sub(t, `Spend${lab}`, v.state === "stale" ? `${ago(v.at)} old` : null));
      body.child(h_flex().px(10).pt(4).gap(6).children([
        this.stat(t, "today", money(s.today)),
        this.stat(t, "7 days", money(s.week)),
        this.stat(t, "per merged PR", money(s.perPR), s.perPR > 10 ? warn(t) : null),
        this.stat(t, "time to merge", s.hours != null ? dur(s.hours * 3600) : "–", t.muted),
      ]));
      const note = [s.pending ? `${s.pending} session${s.pending === 1 ? "" : "s"} not priced yet` : null, s.pollingPct >= 20 ? `${Math.round(s.pollingPct)}% of spend is polling` : null].filter(Boolean).join(" · ");
      if (note) body.child(div().px(12).pt(4).text_size(10.5).text_color(t.dim).child(note));
      if (s.daily.some((x) => x.usd)) {
        body.child(this.sub(t, `Spend per day${lab}`, null));
        body.child(this.bars(t, `spend-days-${v.key}`, s.daily, (x) => x.usd ?? 0, (x) => (x.usd ? money(x.usd).replace(/\.\d\d$/, "") : "0"), t.accent));
      }
      if (s.top.length && !demo()) {
        body.child(this.sub(t, `Most expensive tasks, 7 days${lab}`, null));
        body.children(s.top.map((x) =>
          this.row(t, `cost-${v.key}-${x.key}`, () => openUrl(taskUrl(v.factory, x.key)), [
            div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(String(x.key).split("/").pop()),
            div().flex_none().text_size(11.5).font_semibold().text_color(x.usd > 10 ? warn(t) : t.muted).child(money(x.usd)),
          ])));
      }
    }
    return this.card(t, "Budget & cost", views[0] ? this.btn(t, "spend-open", "Spend ↗", () => openUrl(webUrl("/spend", views[0].factory))) : null, body);
  }

  machinesSection(t, d, body) {
    const rows = machineRows(d);
    if (!rows.length) return;
    const gb = (mb) => (mb / 1024).toFixed(mb >= 10240 ? 0 : 1);
    body.child(this.sub(t, "Machines", rows.length));
    body.children(rows.map((m) => {
      const memUsed = m.mem_total_mb && m.mem_available_mb != null ? 1 - m.mem_available_mb / m.mem_total_mb : null;
      const load = m.load?.[0];
      const loadFrac = load != null && m.cpus ? load / m.cpus : null;
      const tone = (f) => (f == null ? t.dim : f >= 0.95 ? t.blocked : f >= 0.8 ? warn(t) : good(t));
      const facts = [m.os && [m.os, m.arch].filter(Boolean).join(" "), m.cpus ? `${m.cpus} CPUs` : null, m.uptime_s ? `up ${dur(m.uptime_s)}` : null,
        m.agents ? `${m.agents} agent${m.agents === 1 ? "" : "s"}` : null].filter(Boolean).join(" · ");
      const bar = (f) => div().h(4).w_full().rounded(2).bg(t.border).child(div().h_full().w(`${(Math.max(0, Math.min(1, f ?? 0)) * 100).toFixed(1)}%`).rounded(2).bg(tone(f)));
      const cell = (label, value, f) => v_flex().flex_1().min_w(0).gap(3)
        .child(h_flex().gap(4).child(div().text_size(10.5).text_color(t.dim).child(label)).child(div().flex_1()).child(div().text_size(11).text_color(f == null ? t.dim : tone(f)).child(value)))
        .child(bar(f));
      return v_flex().id(`mach-${m.key}`).mx(6).px(6).py(6).gap(5).rounded(6).hover((s) => s.bg(t.hover))
        .child(h_flex().gap(8).items_center()
          .child(div().size(8).rounded(4).flex_none().bg(m.ok ? good(t) : t.blocked))
          .child(div().flex_none().text_size(12.5).font_semibold().text_color(t.foreground).child(m.label))
          .when(Boolean(m.role), (el) => el.child(div().flex_none().px(6).rounded(4).bg(t.raised).text_size(10).text_color(t.muted).child(m.role)))
          .child(div().flex_1().min_w(0).truncate().text_size(10.5).text_color(t.dim).child(m.ok ? facts : `unreachable${m.at ? ` · last seen ${ago(m.at)} ago` : ""}`)))
        .child(h_flex().gap(10)
          .child(cell("memory", m.mem_total_mb ? `${gb(m.mem_total_mb - (m.mem_available_mb ?? 0))}/${gb(m.mem_total_mb)} GB` : "–", memUsed))
          .child(cell("load", load != null ? load.toFixed(1) : "–", loadFrac))
          .when(m.disk_total_gb != null, (el) => el.child(cell("disk", `${m.disk_free_gb} GB free`, m.disk_total_gb ? 1 - m.disk_free_gb / m.disk_total_gb : null)))
          .when(m.swap_total_mb > 0 && !hidden("swap"), (el) => el.child(cell("swap", `${gb(m.swap_used_mb)}/${gb(m.swap_total_mb)} GB`, m.swap_used_mb / m.swap_total_mb))));
    }));
  }

  machinesCard(t, d) {
    const body = v_flex().pb(10);
    this.machinesSection(t, d, body);
    if (!machineRows(d).length) body.child(div().px(12).py(8).text_size(12).text_color(t.dim).child("Loading…"));
    return this.card(t, "Machines", null, body);
  }

  // One factory: its run counts, what needs you, the planner, stuck and failed runs, merges, and its machines.
  factoryCard(t, v) {
    const k = v.key;
    const f = v.factory;
    const stale = v.state === "stale";
    const status = v.state === "down" ? "unreachable" : v.state === "loading" ? "loading…" : stale ? `no answer · data ${ago(v.at)} old` : ago(v.at) === "now" ? "just now" : `${ago(v.at)} ago`;
    const right = h_flex().gap(6).items_center()
      .child(div().text_size(10.5).text_color(v.state === "down" || stale ? t.blocked : t.dim).child(status))
      .when(Boolean(plannerSpace(v)), (el) => el.child(this.btn(t, `${k}-planner`, "Planner", () => showPlanner(v))))
      .child(this.btn(t, `${k}-open`, "Open ↗", () => openUrl(webUrl("/", f))));
    const body = v_flex().pb(10);
    if (!v.loaded) {
      const down = v.state === "down";
      body.child(v_flex().m(10).p(12).gap(4).rounded(8).border(1).border_color(down ? alpha(t.blocked, "66") : t.border)
        .child(div().text_size(12.5).text_color(down ? t.blocked : t.muted).child(down ? `${v.label} has not answered` : `Reaching ${v.label}…`))
        .when(down, (el) => el.child(div().text_size(11).text_color(t.dim).child(`${demo() ? "Not reachable" : v.error}. Check that \`hivemill obs\` runs there, and the factory's url in ~/.config/hivemill/dashboard.json.`))));
      return this.card(t, v.label, right, body, ROLE);
    }
    const c = v.counts;
    const num = (x) => (stale ? t.muted : x);
    if (stale)
      body.child(div().mx(10).mt(10).px(10).py(6).rounded(6).border(1).border_color(alpha(warn(t), "66")).text_size(11.5).text_color(warn(t))
        .child(`Last answer ${ago(v.at)} ago. The numbers below are the last known ones.`));
    body.child(h_flex().px(10).pt(10).gap(6).children([
      this.stat(t, "working", c.working, num(good(t))),
      this.stat(t, "in CI", c.ci, num(null)),
      this.stat(t, "to merge", c.merge, num(null)),
      this.stat(t, "held", c.held, num(c.held ? warn(t) : null)),
      this.stat(t, "needs you", v.needsCount, num(v.needsCount ? warn(t) : null)),
    ]));
    const p = v.planner;
    body.child(h_flex().px(12).pt(8).gap(6).items_center().text_size(11.5)
      .child(div().size(7).rounded(4).flex_none().bg(!p ? t.dim : p.on ? good(t) : warn(t)))
      .child(div().flex_none().text_color(t.foreground).child(!p ? "no planner session" : p.on ? "planner on" : "planner off"))
      .child(div().flex_1().min_w(0).truncate().text_color(t.dim).child(
        [p?.at ? `${p.on ? "session started" : "ended"} ${ago(p.at)} ago` : null, v.projects.length && !demo() ? v.projects.join(", ") : null].filter(Boolean).join(" · "))));

    body.child(this.sub(t, "Needs you", v.needsCount));
    if (!v.needs.length) body.child(div().px(12).py(2).text_size(11.5).text_color(t.dim).child("Nothing needs you."));
    body.children(v.needs.slice(0, 8).map((n, i) =>
      this.row(t, `${k}-need-${i}`, () => openUrl(n.href), [
        div().size(7).rounded(4).flex_none().bg(warn(t)),
        v_flex().flex_1().min_w(0)
          .child(div().truncate().text_size(12).text_color(t.foreground).child(n.kind === "held" ? [demo() ? null : n.task, clean(n.text)].filter(Boolean).join(" · ") : n.text))
          .child(div().truncate().text_size(10.5).text_color(t.dim).child(n.kind === "held" ? ["held", dur(n.age_s), n.pr ? "PR ↗" : "trace ↗"].join(" · ") : "question · open ↗")),
      ])));
    if (v.needs.length > 8) body.child(this.row(t, `${k}-need-more`, () => openUrl(webUrl("/", f)), [div().text_size(11).text_color(t.accent).child(`+${v.needs.length - 8} more in the browser`)]));

    if (v.stuck.length) {
      body.child(this.sub(t, `Over ${stuckLimit(v.stuckAfter)} in one stage`, v.stuck.length));
      body.children(v.stuck.slice(0, 5).map((r) => this.runRow(t, v, `${k}-stuck-${r.key}`, r, warn(t))));
      if (v.stuck.length > 5) body.child(this.row(t, `${k}-stuck-more`, () => openUrl(webUrl("/flow", f)), [div().text_size(11).text_color(t.accent).child(`+${v.stuck.length - 5} more in the browser`)]));
    }
    if (v.failed?.n) {
      body.child(this.sub(t, "Failed today", v.failed.n));
      body.children(v.failed.groups.slice(0, 4).map((g, i) =>
        this.row(t, `${k}-fail-${i}`, () => openUrl(g.keys.length === 1 ? taskUrl(f, g.keys[0].key) : webUrl("/flow", f)), [
          div().flex_none().w(26).text_size(12).font_semibold().text_color(t.blocked).child(String(g.n)),
          div().flex_1().min_w(0).truncate().text_size(12).text_color(t.foreground).child(clean(g.reason)),
          div().flex_none().max_w(150).truncate().text_size(10.5).text_color(t.dim).child(demo() ? "" : g.keys.map((x) => x.task).join(", ") + (g.n > g.keys.length ? ", …" : "")),
        ])));
    }
    if (v.running.length) {
      const rows = [...v.running].sort((a, b) => b.age_s - a.age_s);
      body.child(this.sub(t, "Running, longest in stage first", v.running.length));
      body.children(rows.slice(0, 6).map((r) => this.runRow(t, v, `${k}-run-${r.key}`, r, null)));
      if (rows.length > 6) body.child(this.row(t, `${k}-run-more`, () => openUrl(webUrl("/", f)), [div().text_size(11).text_color(t.accent).child(`+${rows.length - 6} more in the browser`)]));
    }
    const per = v.merged.perDay;
    if (per.length) {
      body.child(this.sub(t, "Merged per day", `${v.merged.today ?? 0} today · ${v.merged.total ?? 0} in ${v.merged.days}d`));
      body.child(this.bars(t, `merge-days-${k}`, per, (x) => x.n ?? 0, (x) => String(x.n ?? 0), stale ? t.muted : good(t)));
    }
    if (v.machines.length) {
      body.child(this.sub(t, "Machines", v.cap != null ? `${v.machines.length} · ${v.cap} sessions at most` : v.machines.length));
      body.children(v.machines.map((m) =>
        h_flex().id(`${k}-m-${m.name}`).px(12).py(3).gap(8).items_center()
          .child(div().size(7).rounded(4).flex_none().bg(m.down ? t.blocked : good(t)))
          .child(div().flex_none().text_size(12).text_color(t.foreground).child(m.label))
          .when(m.coordinator, (el) => el.child(div().flex_none().px(6).rounded(4).bg(t.raised).text_size(10).text_color(t.muted).child("coordinator")))
          .child(div().flex_1().min_w(0).truncate().text_size(11).text_color(m.down ? t.blocked : t.muted).child(
            m.down ? "down" : [`${m.running} running`, m.free_slots != null ? `${m.free_slots} free` : null, m.mem_available_mb != null ? `${(m.mem_available_mb / 1024).toFixed(1)} GB RAM free` : null].filter(Boolean).join(" · ")))
          .child(div().flex_none().text_size(10.5).text_color(t.dim).child(m.last_seen ? `seen ${ago(Date.parse(m.last_seen))}${ago(Date.parse(m.last_seen)) === "now" ? "" : " ago"}` : "no event yet"))));
    }
    return this.card(t, v.label, right, body, ROLE);
  }

  // One run: its task, the stage it is in and for how long. A click goes to its terminal, else to its trace.
  runRow(t, v, id, r, tone) {
    return this.row(t, id, () => openTask(v, r), [
      div().flex_none().w(demo() ? 0 : 56).text_size(11.5).text_color(t.foreground).child(demo() ? "" : r.task),
      div().flex_1().min_w(0).truncate().text_size(11.5).text_color(t.muted).child([r.stage, demo() ? null : r.project].filter(Boolean).join(" · ")),
      div().flex_none().text_size(10.5).text_color(t.dim).child(r.usd != null ? money(r.usd) : ""),
      div().flex_none().w(52).text_size(11).text_color(tone ?? t.muted).child(dur(r.age_s)),
    ]);
  }

  renderFull() {
    const t = theme();
    const d = data();
    const cols = this.width >= 1300 ? 3 : this.width >= 860 ? 2 : 1;
    const col = (cards) => v_flex().flex_none().flex_1().min_w(0).gap(12).children(cards);
    const needs = this.needsCard(t, d), budget = this.budgetCard(t, d), mach = this.machinesCard(t, d);
    const facs = factoryViews(d).map((v) => this.factoryCard(t, v));
    const grid =
      cols === 3 ? h_flex().flex_none().gap(12).items_start().child(col([needs, budget, mach]))
          .child(col(facs.filter((_, i) => i % 2 === 0))).child(col(facs.length > 1 ? facs.filter((_, i) => i % 2 === 1) : []))
      : cols === 2 ? h_flex().flex_none().gap(12).items_start().child(col([needs, budget, mach])).child(col(facs))
      : v_flex().flex_none().gap(12).children([needs, ...facs, budget, mach]);
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
