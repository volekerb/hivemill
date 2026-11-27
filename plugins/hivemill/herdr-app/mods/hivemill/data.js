// Data for the Hivemill dashboard's two faces (status bar and dashboard tab).
// One copy at a time collects (a lease in shared state); every copy reads shared("hivemill.data").
// The source is each factory's read-only obs API (`hivemill obs`: /api/obs/now, flow, machines, spend, task).
// Settings: ~/.config/hivemill/dashboard.json (see dashboard.example.json and docs/herdr-app-dashboard.md).

import { exec, hc, agents, terminals, spaces, machines, readTextFile, home, shared, setShared } from "herdr";

const DATA = "hivemill.data";
const LEASE = "hivemill.lease";
const EVERY = { config: 60_000, now: 30_000, flow: 120_000, mach: 60_000, spend: 300_000, usage: 60_000, scan: 120_000, machines: 60_000 };
const LEASE_MS = 40_000;
export const OBS_URL = "http://127.0.0.1:7461";

export const DEFAULTS = {
  // One entry per factory (a hivemill runner with its own obs server): {url, label, machine, web_url, space}.
  // url: where `hivemill obs` answers. label: the factory's name on this computer. machine: an `hc machine ls` name;
  // when set, the dashboard asks from that machine (curl there), so an obs bound to 127.0.0.1 needs no open port.
  // web_url: what the "Open" links use; default url when machine is unset. space: the Herdr space of the factory's
  // planner ("machine/space" for a space on an hc machine); the card's Planner button selects it.
  factories: [{ url: OBS_URL }],
  // Optional: spaces whose agents' screens are read. space: the Herdr space id or name ("machine/space" on an hc
  // machine). wait_patterns: screen text that means an agent waits on a person, e.g. {"match": "NEEDS REVIEW",
  // "clear": "REVIEWED", "label": "waits for review", "after_min": 15}.
  projects: [],
  // Optional: a JSON file with {five_hour_pct, seven_day_pct, updated_at} for this machine's Claude usage.
  usage_file: null,
  alerts: { context_below_pct: 10, usage_warn_pct: 80, usage_danger_pct: 90, factory_down_minutes: 5, stale_minutes: 3 },
  // A regex with one group: the percent of context left, as the agents' status line prints it.
  context_pattern: "ctx (\\d+)% left",
  // Machines in the Machines card: "auto" (this computer and every `hc machine ls` machine) or a list of names,
  // "local" for this computer. machine_labels: display names, by hc name or by the name a factory reports,
  // e.g. {"local": "Laptop", "box": "Build server"}.
  machines: "auto",
  machine_labels: {},
  // Screens for docs and films: see demo() below.
  demo: { enabled: false, hide: [], alerts: true },
};

// shared() copies the whole stored value on every call, and config(), demo(), hidden() and clean() run once per row:
// one render read it hundreds of times and the app interrupted the render. A render is synchronous, so while one
// runs (oneRead) every data() call reuses a single copy. Outside a render every call reads fresh, as before.
let memo = null;
export const data = () => memo ?? shared(DATA) ?? {};
export function oneRead(fn) {
  if (memo !== null) return fn();
  memo = shared(DATA) ?? {};
  try {
    return fn();
  } finally {
    memo = null;
  }
}
export const config = () => data().config ?? DEFAULTS;

function put(patch) {
  const next = { ...(shared(DATA) ?? {}), ...patch }; // a fresh read, never the memo: no lost writes
  setShared(DATA, next);
  if (memo !== null) memo = next;
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
  if (due("usage")) loadUsage(cx);
  if (due("scan")) scanAgents(cx);
  if (due("machines")) loadMachines(cx);
  for (const k of ["now", "flow", "mach", "spend"]) if (due(k)) put({ at: stamp(k) });
  // Each factory is asked in its own spawned fetches, so a slow one does not delay another.
  for (const f of factoriesOf()) {
    if (due("now")) loadNow(cx, f);
    if (due("flow")) loadFlow(cx, f);
    if (due("mach")) loadPart(cx, f, "machines", (r) => ({ mach: { cap: r.cap ?? null, rows: (r.machines ?? []).map(machRow) } }));
    if (due("spend")) loadPart(cx, f, "spend?days=7&by=task", (r) => ({ spend: spendOf(r.spend ?? {}) }));
  }
}

// ---- factories -----------------------------------------------------------------------------------------------------
// A factory is one hivemill runner and its obs server. "factories" in the settings lists them.

export const ROLE = "factory"; // the tag on a factory's card and machine; a tag, never part of a name

const isLoopback = (url) => /^https?:\/\/(127\.0\.0\.1|localhost|\[::1\])(:|\/|$)/.test(url ?? "");

export function factoriesOf(cfg = config()) {
  const list = Array.isArray(cfg.factories) && cfg.factories.length ? cfg.factories : DEFAULTS.factories;
  const seen = new Set();
  return list.map((f, i) => {
    const machine = f?.machine || null;
    const url = String(f?.url || OBS_URL).replace(/\/+$/, "");
    let key = machine ? `${machine} ${url}` : url;
    if (seen.has(key)) key = `${key}#${i}`;
    seen.add(key);
    return { key, index: i, machine, url, web_url: f?.web_url ?? null, label: f?.label ?? null, space: f?.space ?? null };
  });
}

const facState = (d, key) => d.fac?.[key] ?? {};

function putFac(key, patch) {
  const all = data().fac ?? {};
  put({ fac: { ...all, [key]: { ...(all[key] ?? {}), ...patch } } });
}

// The machines, as the dashboard names them: machine_labels first, then the hc name ("This computer" for local).
// Demo mode never shows a raw name or a hidden word: those become "Machine N" / "This computer".
function machineOrder(d) {
  const names = [];
  const add = (n) => n != null && !names.includes(n) && names.push(n);
  for (const m of Object.values(d.machines ?? {})) add(m.name);
  for (const f of factoriesOf()) add(f.machine);
  for (const p of config().projects ?? []) add(resolveSpace(p.space, spaces())?.machine);
  for (const st of Object.values(d.fac ?? {})) for (const m of st?.mach?.rows ?? []) add(m.name);
  return names;
}

export function labelFor(machine, d = data()) {
  const labels = config().machine_labels ?? {};
  const own = machine == null ? labels["local"] : labels[machine];
  if (own && !hidden(own)) return String(own);
  if (machine == null) return "This computer";
  if (demo()) return `Machine ${machineOrder(d).indexOf(machine) + 1 || machineOrder(d).length + 1}`;
  return machine;
}

// What a factory is called: its own "label", else its hc machine's name, else "Factory N". Demo mode shows only
// generic names, unless the label is a machine_labels value.
export function factoryLabel(f, d = data()) {
  const generic = `Factory ${f.index + 1}`;
  if (f.label && !demo()) return String(f.label);
  if (f.label && Object.values(config().machine_labels ?? {}).includes(f.label) && !hidden(f.label)) return String(f.label);
  if (f.machine) return labelFor(f.machine, d);
  return isLoopback(f.url) && !demo() ? labelFor(null, d) : generic;
}

// Where this machine's screen text comes from: an agents() row names its machine, else the address prefix
// ("macmini/..." is the machine's agent), else the one remote space with that id.
function agentMachine(a, spaceList) {
  if (a.machine != null) return a.machine;
  const addr = String(a.address ?? "");
  if (addr.includes("/")) return addr.split("/")[0];
  const same = spaceList.filter((s) => s.id === a.space);
  return same.length && same.every((s) => s.machine != null) ? same[0].machine : null;
}

const spaceMatches = (s, want) => want && (s.id === want || s.name === want);

// A project's space: "machine/space" names a space on an hc machine; an unprefixed name is this computer's space,
// else the one remote space of that name. -> the spaces() row, or null (machine offline, no such space).
export function resolveSpace(want, spaceList = spaces()) {
  if (!want) return null;
  const m = String(want).match(/^([^/]+)\/(.+)$/);
  if (m) {
    const hit = spaceList.find((s) => s.machine === m[1] && (s.id === m[2] || s.name === m[2]));
    if (hit) return hit;
  }
  const same = spaceList.filter((s) => spaceMatches(s, want));
  return same.find((s) => s.machine == null) ?? (same.length === 1 ? same[0] : null);
}

const shownName = (raw) => (demo() ? String(raw).replace(/^[^/]+\//, "") : String(raw));

// The agents whose screens are read: every agent of this computer, plus the agents of the spaces a project names, on
// this computer or an hc machine. Agents in other remote spaces are left alone.
export function scanTargets(agentList = agents(), spaceList = spaces(), termList = terminals(), cfg = config()) {
  const names = new Map(termList.map((t) => [t.address, t.name]));
  const owned = (cfg.projects ?? []).map((p) => ({ p, sp: resolveSpace(p.space, spaceList) })).filter((x) => x.sp);
  const out = [];
  for (const a of agentList) {
    const machine = agentMachine(a, spaceList);
    const proj = owned.find((x) => a.space === x.sp.id && machine === (x.sp.machine ?? null))?.p ?? null;
    if (machine != null && !proj) continue;
    out.push({ agent: a, machine, project: proj, name: shownName(names.get(a.address) || a.tab_name || a.address) });
  }
  return out;
}

// The bar's leftmost label: the machine of the active space.
export const locationLabel = (space, d = data()) => labelFor(space?.machine ?? null, d);

// One machine's facts as key=value lines, on Linux or macOS, with nothing but sh and the base system.
const PROBE = `
os=$(uname -s); echo os=$os; echo arch=$(uname -m)
if [ "$os" = Darwin ]; then
  echo cpus=$(sysctl -n hw.ncpu); echo mem_total_mb=$(( $(sysctl -n hw.memsize) / 1048576 ))
  ps_=$(sysctl -n hw.pagesize); vm_stat | awk -v p=$ps_ '/Pages free|Pages inactive|Pages speculative/ {gsub(/\\./,"",$NF); s+=$NF} END {print "mem_avail_mb=" int(s*p/1048576)}'
  sysctl -n vm.loadavg | awk '{print "load=" $2 "," $3 "," $4}'
  sysctl -n vm.swapusage | awk '{gsub(/M/,""); print "swap_total_mb=" int($3); print "swap_used_mb=" int($6)}'
  b=$(sysctl -n kern.boottime | sed 's/^{ sec = \\([0-9]*\\).*/\\1/'); echo uptime_s=$(( $(date +%s) - b ))
  echo model=$(sysctl -n hw.model 2>/dev/null)
else
  echo cpus=$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN)
  awk '/MemTotal/ {print "mem_total_mb=" int($2/1024)} /MemAvailable/ {print "mem_avail_mb=" int($2/1024)} /SwapTotal/ {t=$2} /SwapFree/ {f=$2} END {print "swap_total_mb=" int(t/1024); print "swap_used_mb=" int((t-f)/1024)}' /proc/meminfo
  awk '{print "load=" $1 "," $2 "," $3}' /proc/loadavg; awk '{print "uptime_s=" int($1)}' /proc/uptime
fi
df -Pk / | awk 'NR==2 {print "disk_total_gb=" int($2/1048576); print "disk_free_gb=" int($4/1048576)}'
`;

function parseProbe(text) {
  const m = {};
  for (const line of (text ?? "").split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) m[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  const n = (k) => (m[k] == null || m[k] === "" || Number.isNaN(Number(m[k])) ? null : Number(m[k]));
  return {
    os: m.os === "Darwin" ? "macOS" : m.os || null, arch: m.arch || null, model: m.model || null, cpus: n("cpus"),
    mem_total_mb: n("mem_total_mb"), mem_available_mb: n("mem_avail_mb"), swap_total_mb: n("swap_total_mb"), swap_used_mb: n("swap_used_mb"),
    load: (m.load ?? "").split(",").map(Number).filter((x) => !Number.isNaN(x)), uptime_s: n("uptime_s"),
    disk_total_gb: n("disk_total_gb"), disk_free_gb: n("disk_free_gb"),
  };
}

// Every machine of this setup: this computer and the app's hc machines (config "machines": "auto", or a list of
// names; "local" is this computer). Each is probed where it runs; one that cannot be reached keeps its last facts.
function loadMachines(cx) {
  put({ at: stamp("machines") });
  cx.spawn(async (cx) => {
    const want = config().machines ?? "auto";
    const known = machines().map((m) => m.name).filter((x) => x != null);
    const names = Array.isArray(want) ? want.map((x) => (x === "local" ? null : x)) : [null, ...known];
    const prev = data().machines ?? {};
    const out = {};
    for (const name of names) {
      const key = name ?? "local";
      try {
        const r = await exec("sh", ["-c", PROBE], { timeout: 15000, on: name ?? undefined });
        if (r.code !== 0 && !r.stdout) throw new Error(r.stderr.trim() || `exit ${r.code}`);
        out[key] = { name, ...parseProbe(r.stdout), ok: true, at: Date.now() };
      } catch (e) {
        out[key] = { ...(prev[key] ?? { name }), ok: false, error: String(e?.message ?? e) };
      }
    }
    put({ machines: out });
    cx.notify();
  });
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
      if (text) {
        const own = JSON.parse(text);
        cfg = merge(DEFAULTS, own);
        // Settings written for the old dashboard name a server that no longer exists.
        if (!Array.isArray(own.factories) && (own.teams || own.factory))
          error = 'dashboard.json: list your factories under "factories" ("teams" and "factory" are no longer read)';
      }
    } catch (e) {
      error = `dashboard.json: ${e?.message ?? e}`;
    }
    const before = data().config ? JSON.stringify(factoriesOf(data().config)) : null;
    put({ config: cfg, configError: error });
    // A changed factory address: drop what the old one said and ask the new one now.
    if (before !== null && before !== JSON.stringify(factoriesOf(cfg))) put({ at: { config: Date.now() }, fac: null });
    then?.(cx);
    cx.notify();
  });
}

// GET <factory>/api/obs/<path> as JSON, from the factory's machine when one is configured.
async function factoryGet(f, path, maxS) {
  const url = `${f.url}/api/obs/${path}`;
  // curl, not fetch: a hard timeout when the factory is off the network.
  const r = await exec("curl", ["-sS", "-m", String(maxS), "--connect-timeout", "4", url], { timeout: (maxS + 5) * 1000, on: f.machine || undefined });
  if (r.code !== 0) throw new Error(r.stderr.trim().replace(/^curl: \(\d+\)\s*/, "") || `curl exit ${r.code}`);
  return JSON.parse(r.stdout);
}

export function webUrl(path = "", f = factoriesOf()[0]) {
  const base = f?.web_url || (f?.machine ? null : f?.url) || null;
  return base ? `${String(base).replace(/\/+$/, "")}${path}` : null;
}
export const taskUrl = (f, key) => webUrl(`/task?k=${encodeURIComponent(key)}`, f);

// The Now page: what runs, what is held, the planner, the projects. One answer sets the factory's "last heard" time;
// a failure keeps the last answer and starts the down clock.
function loadNow(cx, f) {
  cx.spawn(async (cx) => {
    try {
      const r = await factoryGet(f, "now", 8);
      putFac(f.key, { now: nowOf(r.page ?? {}), at: Date.now(), error: null, downSince: null });
      cx.notify();
      await loadHeld(f, r.page?.held ?? []);
    } catch (e) {
      putFac(f.key, { error: String(e?.message ?? e), downSince: facState(data(), f.key).downSince ?? Date.now() });
    }
    cx.notify();
  });
}

// Why a run is held, and its PR: the Now page has neither, the task's trace has both (its last run.held step).
async function loadHeld(f, held) {
  const prev = facState(data(), f.key).held ?? {};
  const out = {};
  for (const h of held) {
    const id = `${h.key}@${h.since ?? ""}`;
    if (prev[id]) {
      out[id] = prev[id];
      continue;
    }
    try {
      const tr = await factoryGet(f, `task?k=${encodeURIComponent(h.key)}`, 8);
      const step = [...(tr.steps ?? [])].reverse().find((s) => s.kind === "run.held");
      const pr = step?.links?.pr ?? [...(tr.steps ?? [])].reverse().find((s) => s.links?.pr)?.links.pr ?? null;
      out[id] = { reason: step ? String(step.summary ?? "").replace(/^held:\s*/, "") : null, pr: /^https:\/\//.test(pr ?? "") ? pr : null };
    } catch {}
  }
  putFac(f.key, { held: out });
}

// The Flow page: runs stuck in a stage, merges per day, and today's failed runs. The task list itself is not kept.
function loadFlow(cx, f) {
  cx.spawn(async (cx) => {
    try {
      const r = (await factoryGet(f, "flow?days=14", 20)).flow ?? {};
      putFac(f.key, {
        flow: { stuck: (r.stuck ?? []).map(runRow), stuckAfter: r.stuck_after ?? null, perDay: r.merged_per_day ?? [], merged: r.merged ?? 0, days: r.days ?? 14, at: Date.now() },
      });
      const day = (await factoryGet(f, "flow?days=1", 20)).flow ?? {};
      putFac(f.key, { failed: failedOf(day.tasks ?? []), today: (day.tasks ?? []).slice(0, 200).map(runRow) });
    } catch {}
    cx.notify();
  });
}

// ---- API v1: the board and a task's trace, asked while their tab is open ----------------------------------------

const CARDS = 4; // cards kept per cause; the rest is a count
const PROMPT_CHARS = 2000;
const cardOf = (c) => ({ key: c.key || null, task: c.task, title: c.title || null, project: c.project ?? null, stage: c.stage ?? null, age_sec: c.age_sec ?? 0, note: c.note || null, at: c.at ?? null, pr: c.pr ?? null });

// What is kept of /api/obs/v1/board.
export function boardFrom(b, project = null) {
  const columns = (b.columns ?? []).map((c) => ({
    id: c.id, name: c.name, count: c.count ?? 0,
    groups: (c.groups ?? []).map((g) => ({ cause: g.cause ?? "", bad: Boolean(g.bad), count: g.count ?? 0, cards: (g.cards ?? []).slice(0, CARDS).map(cardOf) })),
  }));
  return { project, at: Date.now(), projects: b.projects ?? [], columns, done: { merged: 0, failed_refiled: 0, failed: 0, cancelled: 0, ...(b.done ?? {}) },
    open: columns.filter((c) => c.id !== "done").reduce((n, c) => n + c.count, 0) };
}

// The board of one factory (project: null = all). A factory on a build before API v1 answers no JSON: board.old.
// A failed ask keeps the last good board of the same project.
export function loadBoard(cx, f, project = null) {
  cx.spawn(async (cx) => {
    let raw = null;
    try {
      raw = await factoryGet(f, `v1/board${project ? `?project=${encodeURIComponent(project)}` : ""}`, 15);
      if (!(raw?.api >= 1) || !Array.isArray(raw.columns)) throw new SyntaxError("no board");
      putFac(f.key, { board: boardFrom(raw, project) });
    } catch (e) {
      const last = facState(data(), f.key).board;
      const keep = last?.columns && (last.project ?? null) === project ? last : { project };
      putFac(f.key, { board: { ...keep, old: e instanceof SyntaxError, error: e instanceof SyntaxError ? null : String(e?.message ?? e), tried: Date.now() } });
    }
    cx.notify();
  });
}

const stepOf = (s) => ({ ts: s.ts, kind: s.kind, role: s.role ?? null, model: s.model ?? null, machine: s.machine ?? null, summary: String(s.summary ?? "").slice(0, 600), usd: s.cost_usd ?? null, pr: s.links?.pr ?? null });

// One task's trace (its steps from dispatch to merge), asked when someone opens it. API v1 adds the title, the PR and
// the start of each prompt's text; an older factory answers the plain trace.
export function loadTrace(cx, f, key) {
  putFac(f.key, { trace: { key, loading: true } });
  cx.spawn(async (cx) => {
    try {
      let tr = null;
      try {
        tr = await factoryGet(f, `v1/task?k=${encodeURIComponent(key)}`, 12);
        if (!(tr?.api >= 1) || !Array.isArray(tr.steps)) tr = null;
      } catch (e) {
        if (!(e instanceof SyntaxError)) throw e;
      }
      const v1 = Boolean(tr);
      tr ??= await factoryGet(f, `task?k=${encodeURIComponent(key)}`, 12);
      const from = Math.max(0, (tr.steps ?? []).length - 400);
      const steps = (tr.steps ?? []).slice(from).map(stepOf);
      for (const [i, p] of Object.entries(tr.prompts ?? {})) {
        const st = steps[Number(i) - from];
        if (st && p?.text) st.prompt = { text: String(p.text).slice(0, PROMPT_CHARS), chars: p.chars ?? String(p.text).length, lines: String(p.text).split("\n").length, cut: Boolean(p.truncated) || String(p.text).length > PROMPT_CHARS };
      }
      putFac(f.key, { trace: { key, v1, task: tr.task, title: tr.title || null, goal: tr.goal ?? null, project: tr.project ?? null, usd: tr.total_usd ?? null,
        pr: tr.pr || null, outcome: tr.outcome ?? null, stage: tr.stage ?? null, steps } });
    } catch (e) {
      putFac(f.key, { trace: { key, error: String(e?.message ?? e) } });
    }
    cx.notify();
  });
}

function loadPart(cx, f, path, pick) {
  cx.spawn(async (cx) => {
    try {
      putFac(f.key, pick(await factoryGet(f, path, 20)));
    } catch {}
    cx.notify();
  });
}

// ---- what is kept of each answer (small: the shared state is copied on every read) ---------------------------------

const runRow = (r) => ({
  key: r.key, task: r.task, goal: r.goal ?? null, project: r.project ?? null, machine: r.machine ?? null, stage: r.stage ?? null,
  title: r.title || null, since: r.since ?? null, age_sec: r.age_sec ?? 0, usd: r.usd ?? null, sessions: r.sessions ?? null, outcome: r.outcome ?? null,
});

function nowOf(p) {
  return {
    running: (p.running ?? []).map(runRow), held: (p.held ?? []).map(runRow),
    asks: (p.open_asks ?? []).filter((a) => a.open > 0).map((a) => ({ project: a.project, open: a.open })),
    planner: (p.planner ?? []).map((x) => ({ machine: x.machine ?? null, last_event: x.last_event ?? null, last_at: x.last_at ?? null, usd: x.usd ?? null })),
    projects: (p.projects ?? []).map((x) => ({
      project: x.project, usd_today: x.usd_today, usd_7d: x.usd_7d, usd_30d: x.usd_30d, active: x.active, merged: x.merged,
      per_pr: x.usd_per_merged_pr, hours: x.median_hours_to_merge, pending: x.cost_pending ?? 0,
    })),
  };
}

const machRow = (m) => ({
  name: m.name, coordinator: Boolean(m.coordinator), down: Boolean(m.down), running: (m.running ?? []).length, free_slots: m.free_slots ?? null,
  mem_available_mb: m.mem_available_mb ?? null, last_seen: m.last_seen || null,
});

function spendOf(s) {
  return {
    usd: s.usd ?? null, days: s.days ?? 7, pollingPct: s.polling_pct ?? null,
    daily: (s.daily ?? []).map((x) => ({ day: x.day, usd: (x.task ?? 0) + (x.judge ?? 0) + (x.planner ?? 0) })),
    top: (s.rows ?? []).filter((x) => x.task_key).sort((a, b) => (b.usd ?? 0) - (a.usd ?? 0)).slice(0, 12).map((x) => ({ key: x.task_key, usd: x.usd, sessions: x.sessions ?? null })),
    roles: (s.roles ?? []).map((x) => ({ role: x.role, usd: x.usd, pollingPct: x.polling_pct ?? null })), unattributed: s.unattributed_usd ?? null,
  };
}

// Today's failed runs, grouped by what failed: "stalled", "gate blocked 3 times in round 1", "budget", ...
export function failedOf(tasks) {
  const rows = tasks.filter((t) => String(t.outcome ?? "").startsWith("FAIL"));
  const groups = new Map();
  for (const t of rows) {
    const reason = String(t.outcome).replace(/^FAIL:?\s*/, "").split(":")[0].trim() || "failed";
    const g = groups.get(reason) ?? { reason, n: 0, keys: [] };
    g.n++;
    if (g.keys.length < 3) g.keys.push({ key: t.key, task: t.task });
    groups.set(reason, g);
  }
  return { n: rows.length, groups: [...groups.values()].sort((a, b) => b.n - a.n) };
}

function loadUsage(cx) {
  put({ at: stamp("usage") });
  const file = expand(config().usage_file);
  if (!file) return put({ usage: null });
  cx.spawn(async (cx) => {
    try {
      const u = JSON.parse((await readTextFile(file)) ?? "null");
      const at = Number(u?.updated_at) * 1000;
      put({ usage: u ? { five: num(u.five_hour_pct), seven: num(u.seven_day_pct), resets: num(u.seven_day_resets_at), at, stale: !at || Date.now() - at > 3600_000 } : null });
    } catch {
      put({ usage: null });
    }
    cx.notify();
  });
}

// Reads the agents' screen tails for what the agents list can't show: an agent waiting on a person (a project's
// wait_patterns), a context window nearly full, an expired login. Scans this computer's agents and the agents of the
// spaces a project names, on hc machines too (see scanTargets).
function scanAgents(cx) {
  put({ at: stamp("scan") });
  cx.spawn(async (cx) => {
    const cfg = config();
    let ctxRe = null;
    try {
      ctxRe = cfg.context_pattern ? new RegExp(cfg.context_pattern, "g") : null;
    } catch {}
    const prev = data().scan ?? {};
    const scan = {};
    for (const { agent: a, machine, project: proj, name } of scanTargets()) {
      try {
        // A remote agent's address names its machine ("macmini/..."), so this read goes to that machine.
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
          address: a.address, name, machine, space: a.space, project: proj?.name ?? null, status: a.status, ctx: ctx ?? null,
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

// Task titles the app already shows: a worker tab is named "T-1234 · title" (a judge's tab "T-1234 · judge").
// -> {task id: [{machine, title}]}. Used until a factory's API carries the title itself.
export function tabTitles(spaceList = spaces()) {
  const out = {};
  for (const s of spaceList)
    for (const tb of s.tabs ?? []) {
      const m = String(tb.name ?? "").match(/^(T-\d+) · (.+)$/);
      if (!m || m[2] === "judge") continue;
      (out[m[1]] ??= []).push({ machine: s.machine ?? null, title: m[2] });
    }
  return out;
}

// A task's title for one factory: a tab on one of the factory's machines, else the only tab with that id.
function titleFor(task, titles, machineSet) {
  const hits = titles[task] ?? [];
  return (hits.find((h) => machineSet.has(h.machine)) ?? (hits.length === 1 ? hits[0] : null))?.title ?? null;
}

// ---- one view per factory ------------------------------------------------------------------------------------------

const WAIT_CI = "ci", WAIT_MERGE = "merge";
const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
// A long reason, cut for a one-line alert or a notification.
export const brief = (text, max = 90) => (String(text ?? "").length > max ? `${String(text).slice(0, max - 1).trimEnd()}…` : String(text ?? ""));

// Everything a factory's card, the bar and the alerts show, from its last answers.
// state: "loading" (no answer yet), "down" (never answered), "stale" (the last answer is old or the factory stopped
// answering) or "live". Counts and rows from a stale factory are its last known ones and are marked so.
export function factoryViews(d = data(), spaceList = spaces()) {
  const now = Date.now();
  const titles = tabTitles(spaceList);
  const A = { ...DEFAULTS.alerts, ...(config().alerts ?? {}) };
  return factoriesOf().map((f) => {
    const st = facState(d, f.key);
    const n = st.now ?? null;
    const label = factoryLabel(f, d);
    const base = { key: f.key, factory: f, label, role: ROLE, error: st.error ?? null, at: st.at ?? null, downSince: st.downSince ?? null };
    if (!n) return { ...base, state: st.error ? "down" : "loading", loaded: false, needsCount: 0, needs: [], stuck: [] };
    const state = st.error || now - st.at > A.stale_minutes * 60_000 ? "stale" : "live";
    const drift = Math.max(0, Math.round((now - st.at) / 1000)); // the answer's ages count on until the next one
    const own = new Set([f.machine, resolveSpace(f.space, spaceList)?.machine, ...(st.mach?.rows ?? []).map((m) => m.name)].filter((x) => x != null));
    const named = (task, title) => title || titleFor(task, titles, own);
    const aged = (r, at = st.at) => ({ ...r, title: named(r.task, r.title), age_s: (r.age_sec ?? 0) + Math.max(0, Math.round((now - at) / 1000)) });
    const running = n.running.map((r) => aged(r));
    const counts = { working: 0, ci: 0, merge: 0, held: n.held.length };
    for (const r of running) counts[r.stage === WAIT_CI ? "ci" : r.stage === WAIT_MERGE ? "merge" : "working"]++;
    const needs = [
      ...n.asks.map((a) => ({ kind: "question", project: a.project, count: a.open, text: `${plural(a.open, "open question")}${a.project && !demo() ? ` · ${a.project}` : ""}`, href: webUrl("/", f) })),
      ...n.held.map((h) => {
        const more = st.held?.[`${h.key}@${h.since ?? ""}`] ?? {};
        return {
          kind: "held", key: h.key, task: h.task, title: named(h.task, h.title), project: h.project, since: h.since, age_s: (h.age_sec ?? 0) + drift,
          text: more.reason || String(h.outcome ?? "held").replace(/^held:\s*/, "held for the "), pr: more.pr ?? null, href: more.pr ?? taskUrl(f, h.key),
        };
      }),
    ];
    const all = n.projects.find((p) => p.project === "all") ?? null;
    const planner = [...n.planner].sort((a, b) => String(b.last_at ?? "").localeCompare(String(a.last_at ?? "")))[0] ?? null;
    const perDay = st.flow?.perDay ?? [];
    return {
      ...base, state, loaded: true, drift, counts, waiting: counts.ci + counts.merge + counts.held, running,
      needs, needsCount: n.asks.reduce((s, a) => s + a.open, 0) + n.held.length,
      stuck: (st.flow?.stuck ?? []).map((r) => aged(r, st.flow.at)), stuckAfter: st.flow?.stuckAfter ?? null,
      failed: st.failed ?? null, trace: st.trace ?? null, board: st.board ?? null,
      today: (st.today ?? []).map((r) => ({ ...r, title: named(r.task, r.title) })),
      planner: planner && { on: !/^session\.(ended|closed)$/.test(planner.last_event ?? ""), at: Date.parse(planner.last_at) || null },
      projects: n.projects.map((p) => p.project).filter((p) => p !== "all" && !/^\(/.test(p)),
      merged: { today: perDay.length ? perDay[perDay.length - 1].n : null, perDay, total: st.flow?.merged ?? null, days: st.flow?.days ?? null },
      spend: { today: all?.usd_today ?? null, week: all?.usd_7d ?? null, perPR: all?.per_pr ?? null, hours: all?.hours ?? null, pending: all?.pending ?? 0,
        daily: (st.spend?.daily ?? []).slice(-7), top: (st.spend?.top ?? []).map((x) => ({ ...x, task: String(x.key).split("/").pop(), title: named(String(x.key).split("/").pop(), null) })), pollingPct: st.spend?.pollingPct ?? null,
        roles: st.spend?.roles ?? [], unattributed: st.spend?.unattributed ?? null },
      machines: (st.mach?.rows ?? []).map((m) => ({ ...m, label: labelFor(m.name, d) })), cap: st.mach?.cap ?? null,
    };
  });
}

// ---- the trace as the Traces tab shows it ---------------------------------------------------------------------------

const PHASE = { build: "Build", gate: "Gate", ci: "CI", judge: "Judge", merge: "Merge", end: "End" };
function phaseOf(st, cur) {
  const k = String(st.kind ?? "");
  if (k === "task.dispatched") return "build";
  if (k === "task.failed" || k === "task.cancelled") return "end";
  if (st.role === "judge" || k.startsWith("judge.")) return "judge";
  if (k.startsWith("gate.") || k.startsWith("review.")) return "gate";
  if (k.startsWith("ci.")) return "ci";
  if (k.startsWith("pr.") || k.startsWith("merge.") || k === "run.held") return "merge";
  if (k === "prompt.sent" && / first /.test(` ${st.summary} `)) return cur === "gate" || cur === "ci" || cur === "merge" ? cur : "build";
  return cur ?? "build";
}
const BAD_STEP = /^(gate\.blocked|ci\.red|task\.failed|run\.held|run\.stalled)$/;
const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);
function stepText(st) {
  if (st.kind === "prompt.sent") {
    const kind = (/^prompt sent: (\S+)/.exec(st.summary) ?? [])[1] ?? "";
    const to = st.role === "judge" ? "the judge" : st.role === "task" ? "the task session" : `the ${st.role ?? "session"}`;
    return kind === "first" ? `First prompt to ${to}` : `Prompt to ${to}${kind && kind !== st.role ? `: ${kind}` : ""}`;
  }
  return cap(String(st.summary ?? ""));
}

// A trace laid out as phases (Build, Gate, CI, Judge, Merge), each with its time and cost. Steps that repeat one
// after another (a gate deferred ten times) become one row with a count. now: ms, for a task still running.
export function traceView(tr, now = Date.now()) {
  const steps = tr?.steps ?? [];
  const phases = [];
  let cur = null;
  for (const st of steps) {
    const ph = phaseOf(st, cur);
    if (ph !== cur || !phases.length) phases.push({ id: ph, name: PHASE[ph], from: Date.parse(st.ts) || null, usd: 0, rows: [] });
    cur = ph;
    const p = phases[phases.length - 1];
    p.usd += st.usd ?? 0;
    const text = stepText(st);
    const last = p.rows[p.rows.length - 1];
    if (last && last.text === text && !st.prompt && !last.prompt) {
      last.times++;
      last.usd = (last.usd ?? 0) + (st.usd ?? 0) || null;
      continue;
    }
    p.rows.push({ ts: st.ts, text, times: 1, bad: BAD_STEP.test(st.kind) || /^judge REJECT/.test(st.summary), usd: st.usd, pr: st.pr, prompt: st.prompt ?? null });
  }
  const first = Date.parse(steps[0]?.ts) || null, lastTs = Date.parse(steps[steps.length - 1]?.ts) || null;
  const o = String(tr?.outcome ?? "");
  const open = !o || o.startsWith("held");
  const end = open ? now : lastTs;
  phases.forEach((p, i) => { p.sec = p.from ? Math.max(0, Math.round(((phases[i + 1]?.from ?? end ?? p.from) - p.from) / 1000)) : null; });
  const n = (re) => steps.filter((s) => re.test(s.kind)).length;
  const state = o.startsWith("held") ? "held" : o === "merged" ? "merged" : o.startsWith("FAIL") ? `failed: ${o.replace(/^FAIL:?\s*/, "").split(":")[0]}` : o || STAGE_WORDS[tr?.stage] || "running";
  return {
    phases, state, open, sec: first && end ? Math.max(0, Math.round((end - first) / 1000)) : null,
    sessions: n(/^session\.started$/), rounds: n(/^gate\.(passed|blocked)$/), blocks: n(/^gate\.blocked$/), prompts: n(/^prompt\.sent$/),
  };
}

// The Traces tab's lists, from the board: what needs a look (held, or under a cause marked as trouble), what runs,
// and today's merged and failed tasks. Without a board (an older factory) it falls back to the Now and Flow answers.
export function traceLists(v) {
  const b = v?.board?.columns ? v.board : null;
  const uniq = (rows) => { const seen = new Set(); return rows.filter((r) => r.key && !seen.has(r.key) && seen.add(r.key)); };
  if (!b) {
    const fail = (r) => String(r.outcome ?? "").startsWith("FAIL");
    const row = (r, why) => ({ key: r.key, task: r.task, title: r.title, why });
    return {
      look: uniq([...v.needs.filter((n) => n.kind === "held").map((n) => row(n, `held ${dur(n.age_s)}`)), ...v.stuck.map((r) => row(r, `${STAGE_WORDS[r.stage] ?? r.stage} ${dur(r.age_s)}`))]),
      running: uniq(v.running.map((r) => row(r, `${STAGE_WORDS[r.stage] ?? r.stage} ${dur(r.age_s)}`))),
      merged: uniq(v.today.filter((r) => r.outcome === "merged").map((r) => row(r, "merged"))),
      failed: uniq(v.today.filter(fail).map((r) => row(r, `failed: ${String(r.outcome).replace(/^FAIL:?\s*/, "").split(":")[0]}`))),
    };
  }
  const drift = Math.max(0, Math.round((Date.now() - b.at) / 1000));
  const out = { look: [], running: [], merged: [], failed: [] };
  for (const c of b.columns) for (const g of c.groups) for (const k of g.cards) {
    const word = c.id === "held" ? "held" : c.id === "merge" ? "to merge" : c.id === "ci" ? "in CI" : c.id;
    const row = { key: k.key, task: k.task, title: k.title, why: c.id === "done" ? (k.note ? `failed: ${k.note}` : "merged") : `${g.cause && c.id !== "held" ? g.cause.toLowerCase() : word}, ${dur(k.age_sec + drift)}` };
    if (c.id === "done") out[k.note ? "failed" : "merged"].push(row);
    else if (c.id !== "queued") {
      out.running.push(row);
      if (c.id === "held" || (g.bad && g.cause)) out.look.push(row);
    }
  }
  for (const k of Object.keys(out)) out[k] = uniq(out[k]);
  return out;
}
export const STAGE_WORDS = { dispatched: "working", gate: "in the gate", judge: "with the judge", ci: "in CI", merge: "to merge", held: "held" };

// The status bar's factory segments: "<label> 11 working · 14 waiting", "<label> unreachable" or "<label>…".
export function barSegments(d = data()) {
  return factoryViews(d).map((v) => ({
    key: v.key, label: v.label, state: v.state, needs: v.state === "live" ? v.needsCount : 0,
    text: !v.loaded ? (v.state === "down" ? `${v.label} unreachable` : `${v.label}…`)
      : `${v.label} ${v.counts.working} working · ${v.waiting} waiting${v.state === "stale" ? ` · ${ago(v.at)} old` : ""}`,
  }));
}

// Spend today over every factory that answers; null when none has.
export function spendToday(d = data()) {
  const xs = factoryViews(d).filter((v) => v.loaded && v.state === "live" && v.spend.today != null).map((v) => v.spend.today);
  return xs.length ? xs.reduce((a, b) => a + b, 0) : null;
}

// The open terminal of a task's session, when exactly one is named for it ("claude-task-t-2043").
export function workerTerminal(task, termList = terminals()) {
  const n = Number(String(task ?? "").replace(/^T-/i, ""));
  if (!Number.isFinite(n) || !String(task ?? "").trim()) return null;
  const hits = termList.filter((t) => {
    const m = String(t.name ?? "").match(/^claude-(?:task|coder|judge)-t-0*(\d+)\b/);
    return m && Number(m[1]) === n && t.state !== "dead";
  });
  return hits.length === 1 ? hits[0].address : null;
}

// This machine's Claude usage, from usage_file. stale: the file is over an hour old.
export function usage(d = data()) {
  const u = d.usage;
  return { seven: u?.seven ?? null, five: u?.five ?? null, resets: u?.resets ?? null, at: u?.at ?? null, stale: Boolean(u?.stale) };
}

// Things worth interrupting a person for, most urgent first. key: stable id for "already told". Every factory alert
// comes from an answer that is still fresh; a factory that stopped answering raises only its own "unreachable".
export function alerts(d = data()) {
  const A = { ...DEFAULTS.alerts, ...(config().alerts ?? {}) };
  const out = [];
  const now = Date.now();
  const scan = Object.values(d.scan ?? {});
  const views = factoryViews(d);
  const live = views.filter((v) => v.state === "live");
  // An agent on another machine is named with that machine ("Mac mini · agent ...").
  const who = (s) => (s.machine != null ? `${labelFor(s.machine, d)} · ${s.name}` : s.name);
  for (const s of scan) if (s.login) out.push({ key: `login:${s.address ?? s.name}`, level: "danger", text: `${who(s)}: login expired, run /login`, agent: s });
  const u = usage(d);
  if (!u.stale && u.seven != null && u.seven >= A.usage_danger_pct) out.push({ key: `usage-danger`, level: "danger", text: `Claude weekly usage ${Math.round(u.seven)}%` });
  else if (!u.stale && u.seven != null && u.seven >= A.usage_warn_pct) out.push({ key: `usage-warn`, level: "warning", text: `Claude weekly usage ${Math.round(u.seven)}%` });
  for (const v of live)
    for (const n of v.needs)
      out.push(n.kind === "question"
        ? { key: `ask:${v.key}:${n.project}:${n.count}`, level: "danger", text: `${v.label}: ${n.text}`, href: n.href, factory: v.key }
        : { key: `held:${v.key}:${n.key}:${n.since}`, level: "warning", text: `${v.label}: ${demo() ? "" : `${n.task} `}held · ${brief(n.text)} · ${dur(n.age_s)}`, href: n.href, factory: v.key });
  for (const s of scan)
    for (const [k, w] of Object.entries(s.waits ?? {}))
      if (now - w.since >= w.after_min * 60_000)
        out.push({ key: `wait:${s.address ?? s.name}:${k}:${w.since}`, level: "danger", text: `${who(s)} ${w.label} · ${ago(w.since)}`, agent: s });
  for (const v of views)
    if (v.downSince && now - v.downSince >= A.factory_down_minutes * 60_000)
      out.push({ key: `factory-down:${v.key}`, level: "warning", text: `${v.label} unreachable · ${ago(v.downSince)}`, factory: v.key });
  for (const v of live) {
    for (const m of v.machines) if (m.down) out.push({ key: `fmachine-down:${v.key}:${m.name}`, level: "warning", text: `${v.label}: machine ${m.label} is down`, href: webUrl("/machines", v.factory), factory: v.key });
    const s = v.stuck[0];
    if (s) out.push({
      key: `stuck:${v.key}:${s.key}`, level: "info", factory: v.key, href: webUrl("/flow", v.factory),
      text: `${v.label}: ${plural(v.stuck.length, "run")} over ${stuckLimit(v.stuckAfter)} in one stage · oldest ${demo() ? "" : `${s.task}, `}${s.stage} ${dur(s.age_s)}`,
    });
  }
  for (const s of scan)
    if (s.ctx != null && s.ctx < A.context_below_pct) out.push({ key: `ctx:${s.address ?? s.name}`, level: "warning", text: `${who(s)} context ${s.ctx}% left`, agent: s });
  for (const r of machineRows(d)) {
    if (!r.ok) out.push({ key: `machine-down:${r.key}`, level: "warning", text: `${r.label} unreachable` });
    if (r.disk_free_gb != null && r.disk_total_gb && (r.disk_free_gb < 20 || r.disk_free_gb / r.disk_total_gb < 0.05))
      out.push({ key: `disk:${r.key}:${new Date().toDateString()}`, level: r.disk_free_gb < 5 ? "danger" : "warning", text: `${r.label}: ${r.disk_free_gb} GB disk free` });
    if (r.ok && r.swap_total_mb && r.swap_used_mb / r.swap_total_mb >= 0.95)
      out.push({
        key: `swap:${r.key}:${new Date().toDateString()}`, level: r.mem_available_mb < 2048 ? "danger" : "warning",
        text: `${r.label} swap full (${(r.swap_used_mb / 1024).toFixed(1)} GB) · ${((r.mem_available_mb ?? 0) / 1024).toFixed(1)} GB RAM free`,
      });
  }
  if (d.configError) out.push({ key: `config:${d.configError}`, level: "warning", text: d.configError });
  if (demo()) return config().demo?.alerts === false ? [] : out.filter((a) => !hidden(a.text)).map((a) => ({ ...a, text: clean(a.text) }));
  return out;
}

// "1h0m0s" (the API's stuck limit) as "1h".
export const stuckLimit = (s) => String(s ?? "1h").replace(/(?<=\D)0[ms]/g, "") || "1h";

// Demo mode ("demo": {"enabled": true, "hide": ["word", ...], "alerts": false}): screens for docs and films.
// Task, goal and PR numbers go, agent names lose their task suffix, and any row naming a hidden word is left out.
export const demo = () => Boolean(config().demo?.enabled);
export function hidden(text) {
  if (!demo() || !text) return false;
  const s = String(text).toLowerCase();
  return (config().demo?.hide ?? []).some((w) => w && s.includes(String(w).toLowerCase()));
}
export function clean(text) {
  if (!demo() || text == null) return text;
  return String(text)
    .replace(/\b[TGM]-\d{2,6}[a-z]?\b:?\s*/gi, "")
    .replace(/(^|\s)#\d+\b/g, "$1")
    .replace(/-(t|g)-\d{2,6}[a-z]?\b/gi, "")
    .replace(/\[[A-Z0-9-]+\]\s*/g, "")
    .replace(/^[A-Z]\d+[a-z]?(-[a-z])?\s+(?=\S)/, "")
    .replace(/\s*\((?:[a-z]+-)?\)/gi, "") // "(post-G-001)" emptied above
    .replace(/\s{2,}/g, " ")
    .trim();
}

// The machines this computer can probe, in order: this computer, then hc machines. A machine a factory is reached
// from (its "machine", or its planner space's machine) carries the role tag. -> [{key, label, role, ...facts}]
export function machineRows(d = data()) {
  const fs = factoriesOf();
  const sp = spaces();
  const owns = (f, r) => (f.machine ? r.name === f.machine : f.space && resolveSpace(f.space, sp) ? (resolveSpace(f.space, sp).machine ?? null) === r.name : r.name == null && isLoopback(f.url));
  return Object.entries(d.machines ?? {}).map(([key, m]) => ({
    key, ...m, role: fs.some((f) => owns(f, { name: m.name ?? null })) ? ROLE : null, label: labelFor(m.name ?? null, d), agents: terminalsOn(m.name),
  }));
}

function terminalsOn(name) {
  return terminals().filter((t) => t.state === "live" && t.status && (name == null ? !String(t.address).includes("/") : String(t.address).startsWith(`${name}/`))).length;
}

export const warn = (t) => t.ansi3 ?? t.done;
export const good = (t) => t.ansi2 ?? t.working;
export const levelColor = (t, l) => ({ danger: t.blocked, warning: warn(t), info: t.accent }[l] ?? t.muted);
