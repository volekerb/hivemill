// Data for the Hivemill dashboard's two faces (status bar and dashboard tab).
// One copy at a time collects (a lease in shared state); every copy reads shared("hivemill.data").
// Settings: ~/.config/hivemill/dashboard.json (see config.example.json and docs/herdr-app-dashboard.md).

import { exec, hc, agents, terminals, spaces, readTextFile, home, shared, setShared } from "herdr";

const DATA = "hivemill.data";
const LEASE = "hivemill.lease";
const EVERY = { config: 60_000, factory: 45_000, usage: 60_000, prs: 120_000, scan: 120_000, res: 60_000, cost: 300_000 };
const LEASE_MS = 40_000;

export const DEFAULTS = {
  // url: where the factory web UI answers. machine: an `hc machine ls` name; when set, the dashboard asks the
  // factory from that machine (curl there), so a factory bound to 127.0.0.1 works without opening a port.
  // web_url: what the "Open" buttons use; default url when machine is unset.
  factory: { url: "http://127.0.0.1:8765", machine: null, web_url: null },
  // "auto": show an org (claude, cursor) only while it has a boss, goals or agents. Or a list: ["claude"].
  orgs: "auto",
  // Optional: projects whose agents run in this Herdr app. github: OWNER/NAME for the PR list (needs gh).
  // space: the Herdr space id or name holding the project's agents. wait_patterns: screen text that means an
  // agent waits on a person, e.g. {"match": "NEEDS REVIEW", "clear": "REVIEWED", "label": "waits for review",
  // "after_min": 15}.
  projects: [],
  // Optional: a JSON file with {five_hour_pct, seven_day_pct, updated_at} for this machine's Claude usage.
  usage_file: null,
  alerts: { context_below_pct: 10, stall_minutes: 60, usage_warn_pct: 80, usage_danger_pct: 90, factory_down_minutes: 5 },
  // A regex with one group: the percent of context left, as the agents' status line prints it.
  context_pattern: "ctx (\\d+)% left",
};

export const data = () => shared(DATA) ?? {};
export const config = () => data().config ?? DEFAULTS;

function put(patch) {
  setShared(DATA, { ...data(), ...patch });
}

function merge(base, over) {
  if (!over || typeof over !== "object" || Array.isArray(over)) return over ?? base;
  const out = { ...base };
  for (const [k, v] of Object.entries(over)) out[k] = base && typeof base[k] === "object" && !Array.isArray(base[k]) ? merge(base[k], v) : v;
  return out;
}

const expand = (p) => (typeof p === "string" && p.startsWith("~/") ? `${home()}${p.slice(1)}` : p);

// One collector at a time across all copies; a copy that stops renewing loses the lease.
export function tick(me, cx, force = false) {
  const lease = shared(LEASE);
  const now = Date.now();
  if (!force && lease && lease.id !== me && lease.until > now) return;
  setShared(LEASE, { id: me, until: now + LEASE_MS });
  const at = data().at ?? {};
  const due = (k) => force || !at[k] || now - at[k] >= EVERY[k];
  // Everything else reads the settings, so they load first.
  if (due("config"))
    return loadConfig(cx, (cx) => {
      const at2 = data().at ?? {}; // a changed factory address clears these
      collect(cx, (k) => force || !at2[k] || Date.now() - at2[k] >= EVERY[k]);
    });
  collect(cx, due);
}

function collect(cx, due) {
  if (due("factory")) loadFactory(cx);
  if (due("usage")) loadUsage(cx);
  if (due("prs")) loadPrs(cx);
  if (due("scan")) scanAgents(cx);
  if (due("res"))
    loadJson(cx, "res", "resources", (r) => ({
      res: { rows: r.gate?.rows ?? [], machine: r.machine ?? {}, cursor: r.system?.cursor ?? null, usage: r.system?.usage ?? null },
    }));
  if (due("cost")) {
    loadJson(cx, "cost", "throughput", (r) => ({
      through: { cost: r.cost ?? {}, median_s: r.median_s, p90_s: r.p90_s, merges: r.merges, days: r.days, perDay: r.merges_per_day ?? [] },
    }));
    loadJson(cx, "cost", "tokens", (r) => {
      const rows = (x) => (Array.isArray(x) && Array.isArray(x[0]) ? x[0] : Array.isArray(x) ? x : []); // by_* come as [rows, count]
      return {
        tokens: {
          period: r.period, total: r.totals?.cost ?? null, byOrg: rows(r.by_org), byDay: rows(r.by_day),
          byTask: rows(r.by_task).filter((x) => x.key !== "unattributed").sort((a, b) => (b.cost ?? 0) - (a.cost ?? 0)).slice(0, 8),
        },
      };
    });
  }
}

function stamp(k) {
  return { ...(data().at ?? {}), [k]: Date.now() };
}

function loadConfig(cx, then) {
  put({ at: stamp("config") });
  cx.spawn(async (cx) => {
    let cfg = DEFAULTS, error = null;
    try {
      const text = await readTextFile(`${home()}/.config/hivemill/dashboard.json`);
      if (text) cfg = merge(DEFAULTS, JSON.parse(text));
    } catch (e) {
      error = `dashboard.json: ${e?.message ?? e}`;
    }
    const before = JSON.stringify(data().config?.factory ?? null);
    put({ config: cfg, configError: error });
    // A changed factory address: drop what the old one said and ask the new one now.
    if (before !== "null" && before !== JSON.stringify(cfg.factory)) put({ at: { config: Date.now() }, factory: null, factoryError: null, factoryDownSince: null, res: null, through: null, tokens: null });
    then?.(cx);
    cx.notify();
  });
}

// GET <factory>/api/<path> as JSON, from the factory's machine when one is configured.
async function factoryGet(path, maxS) {
  const f = config().factory ?? DEFAULTS.factory;
  const url = `${(f.url || DEFAULTS.factory.url).replace(/\/$/, "")}/api/${path}`;
  // curl, not fetch: a hard timeout when the factory is off the network.
  const r = await exec("curl", ["-sS", "-m", String(maxS), "--connect-timeout", "4", url], { timeout: (maxS + 5) * 1000, on: f.machine || undefined });
  if (r.code !== 0) throw new Error(r.stderr.trim().replace(/^curl: \(\d+\)\s*/, "") || `curl exit ${r.code}`);
  return JSON.parse(r.stdout);
}

export function webUrl(path = "") {
  const f = config().factory ?? DEFAULTS.factory;
  const base = f.web_url || (f.machine ? null : f.url) || null;
  return base ? `${base.replace(/\/$/, "")}${path}` : null;
}

function loadFactory(cx) {
  put({ at: stamp("factory") });
  cx.spawn(async (cx) => {
    try {
      put({ factory: await factoryGet("home.json", 8), factoryAt: Date.now(), factoryError: null, factoryDownSince: null });
    } catch (e) {
      put({ factoryError: String(e?.message ?? e), factoryDownSince: data().factoryDownSince ?? Date.now() });
    }
    cx.notify();
  });
}

function loadJson(cx, stampKey, name, pick) {
  put({ at: stamp(stampKey) });
  cx.spawn(async (cx) => {
    try {
      put(pick(await factoryGet(`${name}.json`, 20)));
    } catch {}
    cx.notify();
  });
}

function loadUsage(cx) {
  put({ at: stamp("usage") });
  const file = expand(config().usage_file);
  if (!file) return put({ usage: null });
  cx.spawn(async (cx) => {
    try {
      const u = JSON.parse((await readTextFile(file)) ?? "null");
      const at = Number(u?.updated_at) * 1000;
      put({ usage: u ? { five: num(u.five_hour_pct), seven: num(u.seven_day_pct), at, stale: !at || Date.now() - at > 3600_000 } : null });
    } catch {
      put({ usage: null });
    }
    cx.notify();
  });
}

function loadPrs(cx) {
  put({ at: stamp("prs") });
  const projects = (config().projects ?? []).filter((p) => p.github);
  if (!projects.length) return;
  cx.spawn(async (cx) => {
    const out = { ...(data().projects ?? {}) };
    for (const p of projects) {
      try {
        const open = await exec("gh", ["pr", "list", "--repo", p.github, "--state", "open", "--limit", "60", "--json", "number,title,isDraft,updatedAt,url,labels"], { timeout: 30000 });
        if (open.code !== 0) throw new Error(open.stderr.trim() || `gh exit ${open.code}`);
        const merged = await exec("gh", ["pr", "list", "--repo", p.github, "--state", "merged", "--limit", "5", "--json", "number,title,mergedAt,url"], { timeout: 30000 });
        out[p.github] = {
          prs: JSON.parse(open.stdout).map((x) => ({
            number: x.number, title: x.title, draft: x.isDraft, updated: Date.parse(x.updatedAt), url: x.url,
            hold: (x.labels ?? []).some((l) => l.name === "hold"),
          })),
          merged: merged.code === 0 ? JSON.parse(merged.stdout).map((m) => ({ ...m, merged: Date.parse(m.mergedAt) })) : out[p.github]?.merged ?? [],
          error: null,
        };
      } catch (e) {
        out[p.github] = { ...(out[p.github] ?? {}), error: String(e?.message ?? e) };
      }
    }
    put({ projects: out });
    cx.notify();
  });
}

const spaceMatches = (s, want) => want && (s.id === want || s.name === want);

// Reads this machine's agents' screen tails for what the agents list can't show: an agent waiting on a person
// (a project's wait_patterns), a context window nearly full, an expired login.
function scanAgents(cx) {
  put({ at: stamp("scan") });
  cx.spawn(async (cx) => {
    const cfg = config();
    const local = spaces().filter((s) => s.machine == null);
    const localIds = new Set(local.map((s) => s.id));
    const names = new Map(terminals().map((t) => [t.address, t.name]));
    const projectOf = (spaceId) => (cfg.projects ?? []).find((p) => local.some((s) => s.id === spaceId && spaceMatches(s, p.space)));
    let ctxRe = null;
    try {
      ctxRe = cfg.context_pattern ? new RegExp(cfg.context_pattern, "g") : null;
    } catch {}
    const prev = data().scan ?? {};
    const scan = {};
    for (const a of agents()) {
      if (!localIds.has(a.space)) continue;
      const name = names.get(a.address) || a.tab_name || a.address;
      const proj = projectOf(a.space);
      try {
        const r = await hc(["read", a.address, "--lines", "30"]);
        const text = r.stdout ?? "";
        const ctx = ctxRe ? [...text.matchAll(ctxRe)].map((m) => Number(m[1])).filter((n) => !Number.isNaN(n)).pop() : undefined;
        const idle = a.status === "done" || a.status === "idle";
        const waits = {};
        for (const w of proj?.wait_patterns ?? []) {
          if (idle && w.match && text.includes(w.match) && !(w.clear && text.includes(w.clear))) {
            const key = w.match;
            waits[key] = { label: w.label || `waits: ${w.match}`, after_min: w.after_min ?? 15, since: prev[a.address]?.waits?.[key]?.since ?? Date.now() };
          }
        }
        scan[a.address] = {
          address: a.address, name, space: a.space, project: proj?.name ?? null, status: a.status, ctx: ctx ?? null,
          login: /Login expired|Please run \/login/i.test(text), waits,
        };
      } catch {
        if (prev[a.address]) scan[a.address] = prev[a.address];
      }
    }
    put({ scan, scanAt: Date.now() });
    cx.notify();
  });
}

const num = (v) => (v == null || v === "" || Number.isNaN(Number(v)) ? null : Number(v));

export function ago(ms) {
  if (ms == null || Number.isNaN(ms)) return "";
  const m = Math.floor(Math.max(0, Date.now() - ms) / 60000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 && h < 10 ? `${h}h ${m % 60}m` : `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

export const money = (v) => (v == null || Number.isNaN(Number(v)) ? "–" : Number(v) >= 100 ? `$${Math.round(v)}` : `$${Number(v).toFixed(2)}`);

export function dur(s) {
  if (s == null) return "–";
  const m = Math.round(s / 60);
  return m < 60 ? `${m}m` : m < 1440 ? `${Math.floor(m / 60)}h ${m % 60}m` : `${Math.round(m / 1440)}d`;
}

// The orgs to show: the config's list, or (auto) those the factory reports in use.
export function orgsShown(d = data()) {
  const want = config().orgs;
  if (Array.isArray(want)) return new Set(want);
  const lanes = d.factory?.lanes ?? [];
  const used = lanes.filter((l) => l.boss?.status === "live" || l.director?.status === "live" || l.goals_active || l.goals_open || l.agents_live);
  return new Set((used.length ? used : lanes).map((l) => l.org));
}

export function pipeline(github, d = data()) {
  const p = d.projects?.[github] ?? {};
  const prs = p.prs ?? [];
  const ready = prs.filter((x) => !x.draft && !x.hold);
  return { loaded: !!p.prs, error: p.error, prs, ready, drafts: prs.filter((x) => x.draft).length, merged: p.merged ?? [], lastMerge: (p.merged ?? [])[0]?.merged ?? null };
}

// The usage figures to show: this machine's file when configured, else the factory's.
export function usage(d = data()) {
  const f = d.res?.usage ?? d.factory?.system?.usage;
  const u = d.usage;
  return {
    seven: u && !u.stale ? u.seven : f?.seven_day ?? null,
    five: u && !u.stale ? u.five : f?.five_hour ?? null,
    capSeven: f?.cap_seven_day ?? 90,
    capFive: f?.cap_five_hour ?? 92,
    resets: f?.seven_day_resets_at ?? null,
  };
}

// Things worth interrupting a person for, most urgent first. key: stable id for "already told".
export function alerts(d = data()) {
  const A = { ...DEFAULTS.alerts, ...(config().alerts ?? {}) };
  const out = [];
  const now = Date.now();
  const scan = Object.values(d.scan ?? {});
  const orgs = orgsShown(d);
  for (const s of scan) if (s.login) out.push({ key: `login:${s.name}`, level: "danger", text: `${s.name}: login expired, run /login`, agent: s });
  const u = usage(d);
  if (orgs.has("claude") && u.seven != null && u.seven >= A.usage_danger_pct)
    out.push({ key: `usage-danger`, level: "danger", text: `Claude weekly usage ${Math.round(u.seven)}% (cap ${u.capSeven}%)` });
  else if (orgs.has("claude") && u.seven != null && u.seven >= A.usage_warn_pct)
    out.push({ key: `usage-warn`, level: "warning", text: `Claude weekly usage ${Math.round(u.seven)}%` });
  for (const s of scan)
    for (const [k, w] of Object.entries(s.waits ?? {}))
      if (now - w.since >= w.after_min * 60_000)
        out.push({ key: `wait:${s.name}:${k}:${w.since}`, level: "danger", text: `${s.name} ${w.label} · ${ago(w.since)}`, agent: s });
  for (const p of config().projects ?? []) {
    if (!p.github) continue;
    const { ready, lastMerge } = pipeline(p.github, d);
    const working = scan.filter((s) => s.project === p.name && s.status === "working").length;
    if (ready.length && lastMerge && now - lastMerge >= A.stall_minutes * 60_000)
      out.push({ key: `stall:${p.github}:${lastMerge}`, level: working ? "warning" : "danger", text: `${p.name ?? p.github}: no merge for ${ago(lastMerge)} · ${ready.length} PRs open · ${working} working` });
  }
  for (const s of scan)
    if (s.ctx != null && s.ctx < A.context_below_pct) out.push({ key: `ctx:${s.name}`, level: "warning", text: `${s.name} context ${s.ctx}% left`, agent: s });
  const m = d.res?.machine;
  if (m?.swap_total_mb && m.swap_used_mb / m.swap_total_mb >= 0.95)
    out.push({
      key: `swap:${new Date().toDateString()}`, level: m.mem_available_mb < 2048 ? "danger" : "warning",
      text: `Factory swap full (${(m.swap_used_mb / 1024).toFixed(1)} GB) · ${(m.mem_available_mb / 1024).toFixed(1)} GB RAM free`,
    });
  if (d.factoryDownSince && now - d.factoryDownSince >= A.factory_down_minutes * 60_000)
    out.push({ key: `factory-down`, level: "warning", text: `Factory unreachable · ${ago(d.factoryDownSince)}` });
  if (d.configError) out.push({ key: `config:${d.configError}`, level: "warning", text: d.configError });
  return out;
}

export const warn = (t) => t.ansi3 ?? t.done;
export const good = (t) => t.ansi2 ?? t.working;
export const levelColor = (t, l) => ({ danger: t.blocked, warning: warn(t), info: t.accent }[l] ?? t.muted);
