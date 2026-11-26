// Hivemill dashboard for the Herdr app. Two faces from one mod:
//   props {mode: "bar"}  the window's status bar (the "hivemill" layout puts it in place of workspace-status):
//                        where you are, each factory in one line, and the most urgent alert.
//   otherwise            the dashboard tab: what waits for you, the factories as one table, what is worth a
//                        look, the runs, and a narrow column for usage, spend and machines.
// The look follows docs/design/obs-ui.md: no cards and no number tiles, hairlines, one accent, red only for trouble.
// A factory is one hivemill runner; its numbers come from its read-only obs API (`hivemill obs`).
// Agents themselves live in the app's agents list; this shows only what that list can't.
// Settings: ~/.config/hivemill/dashboard.json. Docs: docs/herdr-app-dashboard.md.

import { View, div, svg } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { props, theme, run, exec, spaces, panes, window, keys, notify, nextCommand } from "herdr";
import { data, oneRead, config, tick, ago, money, dur, alerts, usage, webUrl, taskUrl, levelColor, warn, good, demo, hidden, clean, machineRows, factoryViews, barSegments, spendToday, workerTerminal, stuckLimit, locationLabel, resolveSpace, boardOf, BOARD, STAGE_WORDS, loadTrace } from "./data.js";

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
    this.tab = ["board", "traces", "spend"].includes(props()?.view) ? props().view : "overview"; // props {view} opens on that tab
    this.fac = null; // the factory the Board, Traces and Spend tabs show (its key); null = the first
    this.project = null; // the Board's project; null = all
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

  // ---- the dashboard tab ----------------------------------------------------------

  // One accent (honey, the brand's), chosen for the theme's lightness; everything else is the theme's own.
  ink(t) {
    const hex = String(t.background ?? "#161514").replace("#", "");
    const lum = hex.length >= 6 ? (parseInt(hex.slice(0, 2), 16) * 299 + parseInt(hex.slice(2, 4), 16) * 587 + parseInt(hex.slice(4, 6), 16) * 114) / 1000 : 0;
    const dark = lum < 140;
    return { accent: dark ? "#f0a23b" : "#a8650a", ci: dark ? "#8a8578" : "#8c887e", merge: dark ? "#5d594f" : "#c4c0b6", bad: t.blocked, fg: t.foreground, muted: t.muted, dim: t.muted, faint: t.dim, line: t.border, hover: t.hover };
  }

  link(k, id, text, onClick) {
    return div().id(id).flex_none().cursor_pointer().text_color(k.accent).hover((s) => s.text_color(k.fg)).on_click((_e, cx) => onClick(cx)).child(text);
  }

  heading(k, text, more) {
    return h_flex().pt(36).pb(10).gap(8).items_center()
      .child(div().text_size(13).font_semibold().text_color(k.fg).child(text))
      .when(Boolean(more), (el) => el.child(div().text_size(13).text_color(k.dim).child(more)));
  }

  // What waits for a person and nothing else: held runs, open questions, an agent waiting at its terminal.
  waiting(d, views) {
    const out = [];
    for (const v of views.filter((x) => x.state === "live"))
      for (const n of v.needs) {
        if (n.kind === "held") {
          const pr = String(n.pr ?? "").match(/\/pull\/(\d+)/)?.[1];
          out.push({
            id: `held-${v.key}-${n.key}`, title: demo() ? clean(n.title || "A held run") : n.title || n.task, why: clean(n.text),
            meta: [v.label, demo() ? null : n.task, pr && !demo() ? `PR ${pr}` : null].filter(Boolean).join(", "),
            action: n.pr ? "Open PR" : "Open trace", go: () => openUrl(n.href), age: `held ${dur(n.age_s)}`, run: true,
          });
        } else out.push({ id: `ask-${v.key}-${n.project}`, title: n.text, why: "The planner asked and waits for your answer.", meta: v.label, action: "Open", go: () => openUrl(n.href), age: "" });
      }
    for (const a of alerts(d).filter((x) => /^(wait|login):/.test(x.key)))
      out.push({ id: `al-${a.key}`, title: a.text, why: "", meta: "", action: "Show", go: () => follow(a), age: "" });
    return out;
  }

  needsList(k, items) {
    const list = v_flex().border_t(1).border_color(k.line);
    for (const it of items)
      list.child(h_flex().id(it.id).py(14).gap(24).items_center().border_b(1).border_color(k.line)
        .child(v_flex().flex_1().min_w(0).gap(2)
          .child(div().truncate().text_size(15).font_semibold().text_color(k.fg).child(it.title))
          .when(Boolean(it.why), (el) => el.child(div().truncate().text_size(13).text_color(k.muted).child(it.why)))
          .when(Boolean(it.meta), (el) => el.child(div().truncate().text_size(12).text_color(k.dim).child(it.meta))))
        .child(v_flex().flex_none().items_end().gap(4)
          .child(div().id(`${it.id}-go`).cursor_pointer().px(12).py(5).rounded(6).border(1).border_color(k.line).text_size(13).text_color(k.fg)
            .hover((s) => s.border_color(k.accent).text_color(k.accent)).on_click(() => it.go()).child(it.action))
          .when(Boolean(it.age), (el) => el.child(div().text_size(12).text_color(k.dim).child(it.age)))));
    return list;
  }

  // Where a factory's open runs sit, as one thin segmented bar: working, in CI, to merge, held.
  flowBar(k, c, live) {
    const parts = [[c.working, k.accent], [c.ci, k.ci], [c.merge, k.merge], [c.held, k.bad]].filter((x) => x[0] > 0);
    const total = parts.reduce((s, x) => s + x[0], 0);
    const avail = 96 - 2 * Math.max(0, parts.length - 1);
    return h_flex().flex_none().w(96).h(6).gap(2).children(
      parts.map(([n, color]) => div().h_full().rounded(2).w(Math.max(3, Math.round((n / total) * avail))).bg(live ? color : k.dim)));
  }

  factoriesTable(t, k, views) {
    const COLS = [["Working", 64], ["In CI", 52], ["To merge", 68], ["Held", 48], ["Merged", 60], ["Spent", 64]];
    const cell = (w, value, live, bad = false) => h_flex().flex_none().w(w).justify_end()
      .child(value === 0 || value == null
        ? div().text_size(15).text_color(k.faint).child(value == null ? "–" : "0")
        : div().text_size(15).font_semibold().text_color(!live ? k.muted : bad ? k.bad : k.fg).child(String(value)));
    const box = v_flex().rounded(8).border(1).border_color(k.line).bg(t.surface).overflow_hidden();
    box.child(h_flex().px(14).pt(10).pb(8).gap(12).items_center().border_b(1).border_color(k.line).text_size(12).text_color(k.dim)
      .child(div().flex_1().min_w(0).child("Name"))
      .child(div().flex_none().w(96).child("Runs"))
      .children(COLS.map(([label, w]) => h_flex().flex_none().w(w).justify_end().child(label))));
    views.forEach((v, i) => {
      const live = v.state === "live";
      const p = v.planner;
      const sub = !v.loaded ? (v.state === "down" ? `has not answered${demo() || !v.error ? "" : `: ${v.error}`}` : "reaching it…")
        : v.state === "stale" ? `no answer, these numbers are ${ago(v.at)} old`
        : !p ? "no planner session" : p.on ? `planner on${p.at ? `, session started ${ago(p.at)} ago` : ""}` : `planner off${p.at ? `, ended ${ago(p.at)} ago` : ""}`;
      const row = h_flex().id(`fac-${v.key}`).px(14).py(12).gap(12).items_center()
        .child(v_flex().flex_1().min_w(0).gap(1)
          .child(h_flex().gap(10).items_center()
            .child(div().id(`fac-${v.key}-name`).cursor_pointer().truncate().text_size(15).font_semibold().text_color(k.accent).hover((s) => s.text_color(k.fg))
              .on_click(() => openUrl(webUrl("/", v.factory))).child(v.label))
            .when(Boolean(plannerSpace(v)), (el) => el.child(this.link(k, `fac-${v.key}-planner`, "planner tab", () => showPlanner(v)).text_size(12))))
          .child(div().truncate().text_size(12).text_color(live && (!p || p.on) ? k.dim : k.bad).child(sub)));
      if (v.loaded) {
        const c = v.counts;
        row.child(this.flowBar(k, c, live))
          .child(cell(64, c.working, live)).child(cell(52, c.ci, live)).child(cell(68, c.merge, live)).child(cell(48, c.held, live, true))
          .child(cell(60, v.merged.today, live)).child(cell(64, v.spend.today != null ? money(v.spend.today).replace(/\.\d\d$/, "") : null, live));
      } else row.child(div().flex_none().w(96 + COLS.reduce((s, x) => s + x[1] + 12, 0)));
      box.child(i < views.length - 1 ? row.border_b(1).border_color(k.line) : row);
    });
    const key = (color, text) => h_flex().gap(6).items_center().child(div().size(8).rounded(2).bg(color)).child(text);
    return v_flex().gap(10).child(box)
      .child(h_flex().gap(16).text_size(12).text_color(k.dim).children([key(k.accent, "working"), key(k.ci, "in CI"), key(k.merge, "to merge"), key(k.bad, "held")]));
  }

  // Stuck and failed runs as sentences, with one link to the page that lists them.
  lookSection(k, v) {
    const lines = [];
    if (v.stuck.length) {
      const s = v.stuck[0];
      const stages = [...new Set(v.stuck.map((r) => STAGE[r.stage] ?? r.stage))];
      lines.push({ id: "stuck", strong: `${v.stuck.length} run${v.stuck.length === 1 ? " is" : "s are"} over ${stuckLimit(v.stuckAfter)} in one stage`,
        rest: `, ${stages.length === 1 ? `all ${stages[0]}` : stages.join(", ")}. The oldest${demo() ? "" : ` is ${s.task}`} at ${dur(s.age_s)}.`, href: webUrl("/flow", v.factory) });
    }
    if (v.failed?.n) {
      const top = v.failed.groups.slice(0, 3).map((g) => `${g.n} ${clean(g.reason)}`).join(", ");
      lines.push({ id: "failed", strong: `${v.failed.n} run${v.failed.n === 1 ? "" : "s"} failed today.`, rest: ` ${top}${v.failed.groups.length > 3 ? ", and others" : ""}.`, href: webUrl("/flow", v.factory) });
    }
    for (const m of v.machines.filter((x) => x.down)) lines.push({ id: `m-${m.name}`, strong: `Machine ${m.label} is down.`, rest: "", href: webUrl("/machines", v.factory) });
    if (!lines.length) return null;
    return v_flex().child(this.heading(k, `Worth a look on ${v.label}`)).children(lines.map((l) =>
      h_flex().id(`look-${v.key}-${l.id}`).pb(6).gap(6).items_start().text_size(13)
        .child(div().flex_1().min_w(0).text_color(k.muted)
          .child(h_flex().flex_wrap().gap(0).child(div().font_semibold().text_color(k.fg).child(l.strong)).child(div().child(l.rest))))
        .child(this.link(k, `look-${v.key}-${l.id}-go`, "Show them", () => openUrl(l.href)))));
  }

  runsSection(k, v) {
    if (!v.running?.length) return null;
    const over = new Set(v.stuck.map((r) => r.key));
    const rows = [...v.running].sort((a, b) => b.age_s - a.age_s);
    const sec = v_flex().child(this.heading(k, `Running on ${v.label}`, `${rows.length} run${rows.length === 1 ? "" : "s"}, longest in stage first`));
    rows.slice(0, 6).forEach((r, i) => {
      const row = h_flex().id(`run-${v.key}-${r.key}`).py(7).gap(12).items_center().cursor_pointer().hover((s) => s.bg(k.hover)).on_click(() => openTask(v, r))
        .child(div().flex_none().w(demo() ? 0 : 60).text_size(13).text_color(k.dim).child(demo() ? "" : r.task))
        .child(div().flex_1().min_w(0).truncate().text_size(13).text_color(r.title ? k.fg : k.muted).child(r.title ? clean(r.title) : demo() ? "" : r.project ?? ""))
        .child(div().flex_none().w(96).text_size(13).text_color(k.muted).child(STAGE[r.stage] ?? r.stage ?? ""))
        .child(h_flex().flex_none().w(72).justify_end().child(div().text_size(13).text_color(over.has(r.key) ? k.bad : k.muted).child(dur(r.age_s))));
      sec.child(i < Math.min(rows.length, 6) - 1 ? row.border_b(1).border_color(k.line) : row);
    });
    if (rows.length > 6) sec.child(h_flex().pt(8).text_size(13).child(this.link(k, `run-${v.key}-all`, `All ${rows.length} runs`, () => openUrl(webUrl("/", v.factory)))));
    return sec;
  }

  // Every machine with its figures: sessions, memory, load, disk, swap, uptime. A figure turns red past its limit.
  resources(t, k, d, views) {
    const rows = machineRows(d);
    if (!rows.length) return null;
    const gb = (mb) => (mb / 1024).toFixed(mb >= 10240 ? 0 : 1);
    const fac = {};
    for (const v of views.filter((x) => x.loaded)) for (const m of v.machines) fac[m.name] = { ...m, cap: v.cap };
    const COLS = [["Sessions", 84], ["Memory used", 112], ["Load", 92], ["Disk free", 84], ["Swap used", 100], ["Up", 56]];
    const cell = (w, text, bad) => h_flex().flex_none().w(w).justify_end().child(div().text_size(13).text_color(text === "–" ? k.faint : bad ? k.bad : k.fg).child(text));
    const box = v_flex().rounded(8).border(1).border_color(k.line).bg(t.surface).overflow_hidden();
    box.child(h_flex().px(14).pt(10).pb(8).gap(12).items_center().border_b(1).border_color(k.line).text_size(12).text_color(k.dim)
      .child(div().flex_1().min_w(0).child("Machine"))
      .children(COLS.map(([label, w]) => h_flex().flex_none().w(w).justify_end().child(label))));
    rows.forEach((m, i) => {
      const f = fac[m.name ?? ""] ?? null;
      const memUsed = m.mem_total_mb && m.mem_available_mb != null ? 1 - m.mem_available_mb / m.mem_total_mb : null;
      const load = m.load?.[0];
      const swap = m.swap_total_mb > 0 ? m.swap_used_mb / m.swap_total_mb : null;
      const diskLow = m.disk_free_gb != null && m.disk_total_gb && (m.disk_free_gb < 20 || m.disk_free_gb / m.disk_total_gb < 0.05);
      const sub = !m.ok ? `unreachable${m.at ? `, last seen ${ago(m.at)} ago` : ""}`
        : [m.role ? "runs a factory" : null, [m.os, m.arch].filter(Boolean).join(" ") || null, m.cpus ? `${m.cpus} cores` : null].filter(Boolean).join(", ");
      const sessions = f ? (f.down ? "down" : `${f.running}${f.cap != null ? ` of ${f.cap}` : ""}`) : m.agents ? String(m.agents) : "–";
      const row = h_flex().id(`res-${m.key}`).px(14).py(10).gap(12).items_center()
        .child(v_flex().flex_1().min_w(0)
          .child(div().truncate().text_size(13).font_semibold().text_color(k.fg).child(m.label))
          .child(div().truncate().text_size(12).text_color(m.ok ? k.dim : k.bad).child(sub)))
        .child(cell(84, m.ok ? sessions : "–", Boolean(f?.down)))
        .child(cell(112, m.ok && m.mem_total_mb ? `${gb(m.mem_total_mb - (m.mem_available_mb ?? 0))} of ${gb(m.mem_total_mb)} GB` : "–", memUsed != null && memUsed >= 0.92))
        .child(cell(92, m.ok && load != null ? `${load.toFixed(1)}${m.cpus ? ` of ${m.cpus}` : ""}` : "–", load != null && m.cpus && load / m.cpus >= 0.95))
        .child(cell(84, m.ok && m.disk_free_gb != null ? `${m.disk_free_gb} GB` : "–", diskLow))
        .child(cell(100, m.ok && swap != null && !hidden("swap") ? `${gb(m.swap_used_mb)} of ${gb(m.swap_total_mb)} GB` : "–", swap != null && swap >= 0.95))
        .child(cell(56, m.ok && m.uptime_s ? dur(m.uptime_s) : "–", false));
      box.child(i < rows.length - 1 ? row.border_b(1).border_color(k.line) : row);
    });
    return v_flex().child(this.heading(k, "Resources", "sessions are the factory's count; load is the 1-minute average")).child(box);
  }

  aside(t, k, d, views) {
    const col = v_flex();
    const u = usage(d);
    const A = config().alerts ?? {};
    col.child(this.heading(k, "Claude usage"));
    if (u.seven != null) {
      const hot = !u.stale && u.seven >= (A.usage_danger_pct ?? 90);
      const when = u.resets ? ` Resets ${["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"][new Date(u.resets * 1000).getDay()]}.` : "";
      col.child(div().text_size(22).font_semibold().text_color(hot ? k.bad : k.fg).child(pct(u.seven)))
        .child(div().pt(2).text_size(13).text_color(k.muted).child(u.stale ? `of the weekly limit, read ${ago(u.at)} ago.` : `of the weekly limit.${when}${u.five != null ? ` ${pct(u.five)} of the 5-hour window.` : ""}`));
    } else col.child(div().text_size(13).text_color(k.dim).child("Set usage_file in dashboard.json to see it."));

    const live = views.filter((v) => v.loaded);
    if (live.length) {
      const pair = (label, value) => h_flex().py(4).child(div().flex_1().text_size(13).text_color(k.muted).child(label)).child(div().flex_none().text_size(13).text_color(k.fg).child(value));
      const sum = (f) => live.reduce((s, v) => s + (f(v) ?? 0), 0);
      col.child(this.heading(k, "Spend"))
        .child(pair("Today", money(sum((v) => v.spend.today)))).child(pair("Last 7 days", money(sum((v) => v.spend.week))));
      for (const v of live) {
        const lab = live.length > 1 ? `, ${v.label}` : "";
        if (v.spend.perPR != null) col.child(pair(`Per merged PR${lab}`, money(v.spend.perPR)));
        if (v.spend.hours != null) col.child(pair(`Time to merge${lab}`, dur(v.spend.hours * 3600)));
      }
      col.child(h_flex().pt(4).text_size(13).child(this.link(k, "spend-open", "Spend by day and task", () => openUrl(webUrl("/spend", live[0].factory)))));
    }

    return col;
  }

  renderFull() {
    const t = theme();
    const k = this.ink(t);
    const d = data();
    const views = factoryViews(d);
    const items = this.waiting(d, views);
    const runs = items.length && items.every((x) => x.run);
    const noun = runs ? `${items.length} run${items.length === 1 ? "" : "s"}` : `${items.length} thing${items.length === 1 ? "" : "s"}`;
    const heard = views.map((v) => v.at).filter(Boolean);
    const fresh = heard.length ? (ago(Math.min(...heard)) === "now" ? "updated just now" : `updated ${ago(Math.min(...heard))} ago`) : "loading…";
    const notes = alerts(d).filter((a) => /^(factory-down|usage-|config):?/.test(a.key));

    const main = v_flex().flex_1().min_w(0);
    main.child(items.length
      ? h_flex().gap(7).pb(18).text_size(24).font_semibold().child(div().text_color(k.accent).child(noun)).child(div().text_color(k.fg).child(`${items.length === 1 ? "is" : "are"} waiting for you`))
      : div().pb(6).text_size(24).font_semibold().text_color(k.fg).child("Nothing is waiting for you"));
    if (items.length) main.child(this.needsList(k, items));
    for (const a of notes) main.child(div().pt(10).text_size(13).text_color(a.level === "danger" ? k.bad : k.muted).child(a.text));
    main.child(this.heading(k, "Factories", "today")).child(this.factoriesTable(t, k, views));
    const res = this.resources(t, k, d, views);
    if (res) main.child(res);
    for (const v of views.filter((x) => x.loaded)) {
      const look = this.lookSection(k, v);
      if (look) main.child(look);
    }
    for (const v of views.filter((x) => x.loaded)) {
      const sec = this.runsSection(k, v);
      if (sec) main.child(sec);
    }

    const wide = this.width >= 1040;
    const side = this.aside(t, k, d, views);
    const tabs = [["overview", "Overview"], ["board", "Board"], ["traces", "Traces"], ["spend", "Spend"]];
    const header = h_flex().pb(14).gap(14).items_center().text_size(13)
      .child(div().font_semibold().text_color(k.fg).child("Hivemill"))
      .child(div().flex_1().text_color(k.dim).child(`${views.length} factor${views.length === 1 ? "y" : "ies"}, ${fresh}`))
      .child(this.link(k, "refresh", "Refresh", (cx) => tick(this.me, cx, true)));
    const tabRow = h_flex().mb(26).gap(22).border_b(1).border_color(k.line).children(tabs.map(([id, label]) =>
      div().id(`tab-${id}`).cursor_pointer().pb(8).text_size(13)
        .when(this.tab === id, (el) => el.font_semibold().text_color(k.fg).border_b(2).border_color(k.accent))
        .when(this.tab !== id, (el) => el.text_color(k.muted).hover((st) => st.text_color(k.fg)))
        .on_click((_e, cx) => { this.tab = id; cx.notify(); }).child(label)));
    const body = this.tab === "board" ? this.boardTab(t, k, views)
      : this.tab === "traces" ? this.tracesTab(t, k, views)
      : this.tab === "spend" ? this.spendTab(t, k, views)
      : wide ? h_flex().gap(56).items_start().child(main).child(side.flex_none().w(280)) : v_flex().child(main).child(side);
    const page = v_flex().flex_none().w_full().max_w(this.tab === "board" ? 1500 : 1180).mx_auto().px(32).pt(26).pb(56).child(header).child(tabRow).child(body);
    return v_flex().id("root").size_full().bg(t.background)
      .on_size_change((size, cx) => {
        if (Math.abs(size.width - this.width) > 20) {
          this.width = size.width;
          cx.notify();
        }
      })
      .child(v_flex().id("scroll").flex_1().min_h(0).overflow_y_scrollbar().child(page));
  }

  // ---- the other tabs ----------------------------------------------------------------

  // Which factory a tab shows, and the chips that pick it (shown when there is more than one).
  picked(views) {
    const live = views.filter((v) => v.loaded);
    return live.find((v) => v.key === this.fac) ?? live[0] ?? null;
  }

  chips(k, id, items, current, pick) {
    return h_flex().flex_wrap().gap(18).pb(16).text_size(13).children(items.map(([value, label]) =>
      div().id(`${id}-${value ?? "all"}`).cursor_pointer()
        .when(value === current, (el) => el.font_semibold().text_color(k.fg))
        .when(value !== current, (el) => el.text_color(k.muted).hover((st) => st.text_color(k.fg)))
        .on_click((_e, cx) => { pick(value); cx.notify(); }).child(label)));
  }

  empty(k, text) {
    return div().py(18).text_size(13).text_color(k.dim).child(text);
  }

  openTrace(cx, v, key) {
    this.tab = "traces";
    this.fac = v.key;
    loadTrace(cx, v.factory, key);
    cx.notify();
  }

  // Tasks as cards, in the column of where each is now. A click opens the task's trace.
  boardTab(t, k, views) {
    const v = this.picked(views);
    const out = v_flex();
    if (!v) return out.child(this.empty(k, "No factory has answered yet."));
    if (views.length > 1) out.child(this.chips(k, "bf", views.filter((x) => x.loaded).map((x) => [x.key, x.label]), v.key, (key) => { this.fac = key; this.project = null; }));
    const projects = demo() ? [] : v.projects;
    if (projects.length > 1) out.child(this.chips(k, "bp", [[null, "all projects"], ...projects.map((p) => [p, p])], this.project, (p) => { this.project = p; }));
    const cols = boardOf(v, projects.includes(this.project) ? this.project : null);
    const LIMIT = { working: 12, ci: 12, merge: 12, held: 12, done: 14 };
    const box = h_flex().items_start().rounded(8).border(1).border_color(k.line).bg(t.surface).overflow_hidden();
    BOARD.forEach(([id, label], i) => {
      const cards = cols[id];
      const col = v_flex().flex_1().min_w(0).min_h(240)
        .child(h_flex().px(12).pt(10).pb(8).border_b(1).border_color(k.line).text_size(13)
          .child(div().flex_1().font_semibold().text_color(k.fg).child(label)).child(div().text_color(k.dim).child(String(cards.length))));
      cards.slice(0, LIMIT[id]).forEach((c) => col.child(
        v_flex().id(`card-${id}-${c.key}`).px(12).py(9).gap(1).border_b(1).border_color(k.line).cursor_pointer().hover((st) => st.bg(k.hover))
          .on_click((_e, cx) => this.openTrace(cx, v, c.key))
          .when(Boolean(c.title), (el) => el.child(div().truncate().text_size(13).text_color(k.fg).child(clean(c.title ?? ""))))
          .child(h_flex().gap(8).text_size(c.title ? 12 : 13)
            .child(div().flex_none().text_color(c.title ? k.dim : k.fg).child(demo() ? (c.title ? "" : "Task") : c.task))
            .child(h_flex().flex_1().min_w(0).justify_end().child(div().truncate().text_size(12).text_color(c.bad ? k.bad : k.dim).child(clean(c.note ?? "")))))));
      if (!cards.length) col.child(div().px(12).py(10).text_size(12).text_color(k.faint).child("none"));
      if (cards.length > LIMIT[id]) col.child(div().px(12).py(8).text_size(12).text_color(k.dim).child(`and ${cards.length - LIMIT[id]} more`));
      box.child(i < BOARD.length - 1 ? col.border_r(1).border_color(k.line) : col);
    });
    out.child(box);
    out.child(div().pt(10).text_size(12).text_color(k.dim).child("Tasks with an event today. Red says why a card is not moving. Queued tasks appear once the factory's API lists them."));
    return out;
  }

  // One task from dispatch to merge: every step the runner logged, with its cost.
  tracesTab(t, k, views) {
    const v = this.picked(views);
    const out = v_flex();
    if (!v) return out.child(this.empty(k, "No factory has answered yet."));
    if (views.length > 1) out.child(this.chips(k, "tf", views.filter((x) => x.loaded).map((x) => [x.key, x.label]), v.key, (key) => { this.fac = key; }));
    const tr = v.trace;
    const seen = new Set();
    const tasks = [...v.running, ...v.needs.filter((n) => n.kind === "held"), ...v.today].filter((r) => r.key && !seen.has(r.key) && seen.add(r.key));
    const list = v_flex().flex_none().w(300).children(tasks.slice(0, 40).map((r) =>
      v_flex().id(`tl-${r.key}`).px(10).py(7).rounded(6).cursor_pointer().when(tr?.key === r.key, (el) => el.bg(t.surface)).hover((st) => st.bg(k.hover))
        .on_click((_e, cx) => this.openTrace(cx, v, r.key))
        .child(div().truncate().text_size(13).text_color(k.fg).child(r.title ? clean(r.title) : demo() ? "Task" : r.task))
        .child(div().truncate().text_size(12).text_color(k.dim).child([demo() || !r.title ? null : r.task, r.kind === "held" ? "held" : STAGE_WORDS[r.stage] ?? (String(r.outcome ?? "").startsWith("FAIL") ? "failed" : r.outcome)].filter(Boolean).join(", ")))));
    const detail = v_flex().flex_1().min_w(0);
    if (!tr) detail.child(this.empty(k, "Pick a task on the left, or click a card on the Board."));
    else if (tr.loading) detail.child(this.empty(k, "Loading the trace…"));
    else if (tr.error) detail.child(div().py(18).text_size(13).text_color(k.bad).child(`The trace did not load: ${demo() ? "not reachable" : tr.error}`));
    else {
      const title = tasks.find((r) => r.key === tr.key)?.title;
      detail.child(div().text_size(20).font_semibold().text_color(k.fg).child(title ? clean(title) : demo() ? "Task" : tr.task))
        .child(h_flex().pt(4).pb(14).gap(14).text_size(13).text_color(k.muted)
          .child(div().child([demo() ? null : tr.task, demo() ? null : tr.project, `${tr.steps.length} steps`, tr.usd != null ? money(tr.usd) : null].filter(Boolean).join(", ")))
          .child(this.link(k, "tr-open", "Open the full page", () => openUrl(taskUrl(v.factory, tr.key)))));
      const rows = v_flex().border_t(1).border_color(k.line);
      for (const [i, st] of tr.steps.entries()) {
        const bad = /fail|red|blocked|reject|held|stalled/i.test(`${st.kind} ${st.summary}`);
        rows.child(h_flex().id(`st-${i}`).py(7).gap(12).items_start().border_b(1).border_color(k.line).text_size(13)
          .child(div().flex_none().w(46).text_color(k.dim).child(clock(st.ts)))
          .child(div().flex_none().w(52).text_color(k.dim).child(st.role ?? ""))
          .child(div().flex_1().min_w(0).text_color(bad ? k.bad : st.kind === "prompt.sent" ? k.muted : k.fg).child(clean(st.summary)))
          .when(Boolean(st.pr), (el) => el.child(this.link(k, `st-${i}-pr`, "PR", () => openUrl(st.pr))))
          .child(h_flex().flex_none().w(56).justify_end().child(div().text_color(k.muted).child(st.usd != null ? money(st.usd) : ""))));
      }
      detail.child(rows);
      detail.child(div().pt(10).text_size(12).text_color(k.dim).child("Prompt steps name the prompt's file. Its text shows here once the factory's API serves it."));
    }
    return out.child(h_flex().gap(32).items_start().child(list).child(detail));
  }

  // Where the money went: per day, per role, and the most expensive tasks of the last 7 days.
  spendTab(t, k, views) {
    const live = views.filter((v) => v.loaded);
    const out = v_flex();
    if (!live.length) return out.child(this.empty(k, "No factory has answered yet."));
    const sum = (f) => live.reduce((s, v) => s + (f(v) ?? 0), 0);
    out.child(h_flex().gap(8).pb(4).text_size(24).font_semibold().child(div().text_color(k.accent).child(money(sum((v) => v.spend.today)))).child(div().text_color(k.fg).child("spent today")))
      .child(div().pb(8).text_size(13).text_color(k.muted).child(`${money(sum((v) => v.spend.week))} in the last 7 days, over ${live.length} factor${live.length === 1 ? "y" : "ies"}. Prices are API prices.`));
    const table = (id, head, rows) => {
      const box = v_flex();
      box.child(h_flex().pb(8).gap(12).border_b(1).border_color(k.line).text_size(12).text_color(k.dim)
        .children(head.map(([label, w]) => (w ? h_flex().flex_none().w(w).justify_end().child(label) : div().flex_1().min_w(0).child(label)))));
      rows.forEach((r, i) => box.child(h_flex().id(`${id}-${i}`).py(7).gap(12).items_center().border_b(1).border_color(k.line).text_size(13)
        .when(Boolean(r.go), (el) => el.cursor_pointer().hover((st) => st.bg(k.hover)).on_click((_e, cx) => r.go(cx)))
        .children(r.cells.map((c, j) => (head[j][1] ? h_flex().flex_none().w(head[j][1]).justify_end().child(div().text_color(c.color ?? k.fg).child(c.text)) : div().flex_1().min_w(0).truncate().text_color(c.color ?? k.fg).child(c.text))))));
      return box;
    };
    for (const v of live) {
      const s = v.spend;
      const lab = live.length > 1 ? ` on ${v.label}` : "";
      const max = Math.max(1, ...s.daily.map((x) => x.usd ?? 0));
      out.child(this.heading(k, `Per day${lab}`, s.perPR != null ? `${money(s.perPR)} per merged PR` : null));
      const days = v_flex();
      const from = Math.max(0, s.daily.findIndex((x) => x.usd));
      for (const x of s.daily.slice(from)) days.child(h_flex().py(5).gap(12).items_center().text_size(13)
        .child(div().flex_none().w(92).text_color(k.muted).child(String(x.day).slice(5)))
        .child(h_flex().flex_1().child(div().h(8).rounded(2).w(Math.max(x.usd ? 3 : 0, Math.round(((x.usd ?? 0) / max) * 420))).bg(x.usd === max ? k.accent : k.merge)))
        .child(h_flex().flex_none().w(72).justify_end().child(div().text_color(x.usd ? k.fg : k.faint).child(x.usd ? money(x.usd) : "0"))));
      out.child(days);
      if (s.roles.length) out.child(this.heading(k, `By role${lab}`, "last 7 days"))
        .child(table(`role-${v.key}`, [["Role", 0], ["Of it, waiting", 110], ["Spent", 90]], s.roles.map((r) => ({ cells: [{ text: r.role }, { text: r.pollingPct != null ? `${Math.round(r.pollingPct)}%` : "–", color: r.pollingPct >= 30 ? k.bad : k.muted }, { text: money(r.usd) }] }))));
      if (s.top.length && !demo()) out.child(this.heading(k, `Most expensive tasks${lab}`, "last 7 days"))
        .child(table(`top-${v.key}`, [["Task", 0], ["Sessions", 90], ["Spent", 90]], s.top.map((x) => ({
          go: (cx) => this.openTrace(cx, v, x.key),
          cells: [{ text: x.title ? `${x.title}` : x.task, color: k.fg }, { text: x.sessions != null ? String(x.sessions) : "–", color: k.muted }, { text: money(x.usd), color: x.usd > 10 ? k.bad : k.fg }] }))));
      out.child(h_flex().pt(10).text_size(13).child(this.link(k, `sp-${v.key}`, `Open the Spend page${lab}`, () => openUrl(webUrl("/spend", v.factory)))));
    }
    return out;
  }
}

const clock = (ts) => {
  const dt = new Date(ts);
  return Number.isNaN(dt.getTime()) ? "" : `${String(dt.getHours()).padStart(2, "0")}:${String(dt.getMinutes()).padStart(2, "0")}`;
};

// The stages in plain words.
const STAGE = { dispatched: "working", gate: "in the gate", judge: "with the judge", ci: "in CI", merge: "to merge", held: "held" };
