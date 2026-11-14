// Data for the Hivemill dashboard's two faces (status bar and dashboard tab).
// One copy at a time collects (a lease in shared state); every copy reads shared("hivemill.data").
// Settings: ~/.config/hivemill/dashboard.json (see config.example.json and docs/herdr-app-dashboard.md).

import { exec, hc, agents, terminals, spaces, machines, readTextFile, home, shared, setShared } from "herdr";

const DATA = "hivemill.data";
const LEASE = "hivemill.lease";
const EVERY = { config: 60_000, factory: 45_000, usage: 60_000, prs: 120_000, scan: 120_000, res: 60_000, cost: 300_000, machines: 60_000 };
const LEASE_MS = 40_000;

export const DEFAULTS = {
  // url: where the factory web UI answers. machine: an `hc machine ls` name; when set, the dashboard asks the
  // factory from that machine (curl there), so a factory bound to 127.0.0.1 works without opening a port.
  // web_url: what the "Open" buttons use; default url when machine is unset.
  factory: { url: "http://127.0.0.1:8765", machine: null, web_url: null },
  // teams: one entry per Hivemill team (each with its own boss), {machine, url, web_url, label}; machine is an
  // `hc machine ls` name or null for this computer, label overrides the machine's display name. Without teams,
  // the single "factory" entry above is the only team. A team without a Hivemill API is declared per project
  // with projects[].boss (see below).
  teams: [],
  // "auto": show an org (claude, cursor) only while it has a boss, goals or agents. Or a list: ["claude"].
  orgs: "auto",
  // Optional: projects whose agents run in this Herdr app. github: OWNER/NAME for the PR list (needs gh).
  // space: the Herdr space id or name holding the project's agents. wait_patterns: screen text that means an
  // agent waits on a person, e.g. {"match": "NEEDS REVIEW", "clear": "REVIEWED", "label": "waits for review",
  // "after_min": 15}. space may name a space on an hc machine: "macmini/teamb" (machine/space). boss: the name of
  // the project's boss terminal, or {"match": "<regex>"}; the dashboard then shows that boss and the project's
  // other agents as its lanes, for a team that runs no Hivemill API.
  projects: [],
  // Optional: a JSON file with {five_hour_pct, seven_day_pct, updated_at} for this machine's Claude usage.
  usage_file: null,
  alerts: { context_below_pct: 10, stall_minutes: 60, usage_warn_pct: 80, usage_danger_pct: 90, factory_down_minutes: 5 },
  // A regex with one group: the percent of context left, as the agents' status line prints it.
  context_pattern: "ctx (\\d+)% left",
  // Machines in the Resources card: "auto" (this computer and every `hc machine ls` machine) or a list of names,
  // "local" for this computer. machine_labels: display names, e.g. {"local": "Laptop", "box": "Build server"}.
  machines: "auto",
  machine_labels: {},
  // Screens for docs and films: see demo() below.
  demo: { enabled: false, hide: [], alerts: true },
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
  if (due("machines")) loadMachines(cx);
  // Each team is asked in its own spawned fetch, so a slow team does not delay another.
  for (const team of teamsOf()) {
    if (due("res"))
      loadJson(cx, "res", team, "resources", (r) => ({
        res: { rows: r.gate?.rows ?? [], machine: r.machine ?? {}, cursor: r.system?.cursor ?? null, usage: r.system?.usage ?? null },
      }));
    if (due("cost")) {
      loadJson(cx, "cost", team, "throughput", (r) => ({
        through: { cost: r.cost ?? {}, median_s: r.median_s, p90_s: r.p90_s, merges: r.merges, days: r.days, perDay: r.merges_per_day ?? [] },
      }));
      loadJson(cx, "cost", team, "tokens", (r) => {
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
}

// ---- teams -------------------------------------------------------------------------------------------------------
// A team is one Hivemill API (with its own boss, lanes, goals). "teams" in the settings lists them; the old single
// "factory" entry is read as the only team when "teams" is absent.

export const ROLE = "factory"; // the role tag on a team's machine; a tag, never part of a name

const isLoopback = (url) => /^https?:\/\/(127\.0\.0\.1|localhost|\[::1\])(:|\/|$)/.test(url ?? "");

export function teamsOf(cfg = config()) {
  const list = Array.isArray(cfg.teams) && cfg.teams.length ? cfg.teams : [cfg.factory ?? DEFAULTS.factory];
  const seen = new Set();
  return list.map((t, i) => {
    const machine = t?.machine || null;
    let key = machine ?? "local";
    if (seen.has(key)) key = `${key}#${i}`;
    seen.add(key);
    return { key, machine, url: t?.url || DEFAULTS.factory.url, web_url: t?.web_url ?? null, label: t?.label ?? null };
  });
}

const teamState = (d, key) => d.team?.[key] ?? {};

function putTeam(key, patch) {
  const all = data().team ?? {};
  put({ team: { ...all, [key]: { ...(all[key] ?? {}), ...patch } } });
}

// The machines, as the dashboard names them: machine_labels first, then the hc name ("This computer" for local).
// Demo mode never shows a raw hc name or a hidden word: those become "Machine N" / "This computer".
function machineOrder(d) {
  const names = [];
  const add = (n) => n != null && !names.includes(n) && names.push(n);
  for (const m of Object.values(d.machines ?? {})) add(m.name);
  for (const t of teamsOf()) add(t.machine);
  for (const p of config().projects ?? []) add(resolveSpace(p.space, spaces())?.machine);
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

// What a team is called: its own "label", else its machine's name. A Hivemill team reached by a network URL (no hc
// machine, not this computer) has no machine name to show, so it needs a "label".
export function teamLabel(team, d = data()) {
  if (team.label && !hidden(team.label)) return String(team.label);
  if (team.machine || isLoopback(team.url)) return labelFor(team.machine, d);
  return "Server";
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

const bossMatches = (boss, row) => {
  const names = [row.name, row.tab, row.address].filter(Boolean);
  if (typeof boss === "string") return names.includes(boss);
  try {
    const re = new RegExp(boss?.match ?? "(?!)");
    return names.some((n) => re.test(n));
  } catch {
    return false;
  }
};

// A team with no Hivemill API, declared by projects[].boss: its boss and lanes come from the app's agents list.
export function projectTeams(cfg = config(), spaceList = spaces(), agentList = agents(), termList = terminals(), d = data()) {
  const names = new Map(termList.map((t) => [t.address, t.name]));
  const out = [];
  for (const p of cfg.projects ?? []) {
    if (!p.boss) continue;
    const sp = resolveSpace(p.space, spaceList);
    if (!sp) continue;
    const machine = sp.machine ?? null;
    const row = (a) => ({ address: a.address, tab: a.tab_name, name: clean(shownName(names.get(a.address) || a.tab_name || a.address)), status: a.status ?? "unknown" });
    const onMachine = agentList.filter((a) => agentMachine(a, spaceList) === machine);
    const members = onMachine.filter((a) => a.space === sp.id).map(row);
    const boss = members.find((r) => bossMatches(p.boss, r)) ?? onMachine.map(row).find((r) => bossMatches(p.boss, r)) ?? null;
    out.push({
      key: `project:${p.name ?? p.space}`, kind: "project", machine, label: labelFor(machine, d), project: p.name ?? null,
      role: null, boss, lanes: members.filter((r) => r !== boss),
    });
  }
  return out;
}

// What a lane says: its boss and director, then its counts.
const laneText = (l) =>
  [l.boss ? `boss ${l.boss.status ?? "–"}` : null, l.director ? `director ${l.director.status ?? "–"}` : null,
    `${l.workers_live ?? 0} workers · ${l.leads_awake ?? 0} leads awake · ${l.goals_active ?? 0} goals`].filter(Boolean).join(" · ");

// The teams to show, in order: each Hivemill team, then each project-declared one. One team's trouble stays its own.
export function teamViews(d = data(), inp = {}) {
  const views = teamsOf().map((team) => {
    const st = teamState(d, team.key);
    const orgs = orgsShown(d, team.key);
    const home = st.home ?? null;
    return {
      key: team.key, kind: "hivemill", team, machine: team.machine, label: teamLabel(team, d), role: ROLE, orgs, state: st, home,
      error: st.error ?? null, downSince: st.downSince ?? null, at: st.at ?? null,
      lanes: (home?.lanes ?? []).filter((l) => orgs.has(l.org)).map((l) => ({ org: l.org, label: l.label ?? l.org, boss: l.boss?.status ?? null, director: l.director?.status ?? null, tone: l.health?.tone, text: laneText(l), health: l.health?.text ?? "" })),
    };
  });
  return [...views, ...projectTeams(config(), inp.spaces ?? spaces(), inp.agents ?? agents(), inp.terminals ?? terminals(), d)];
}

// The status bar's team segments: "<label> factory 3 live", "<label> offline", "<label>…", or a project team's boss.
export function barSegments(d = data(), inp = {}) {
  return teamViews(d, inp).map((v) => {
    if (v.kind === "project") {
      const live = v.lanes.filter((l) => l.status === "working" || l.status === "idle" || l.status === "done" || l.status === "blocked").length;
      return { key: v.key, kind: v.kind, label: v.label, tag: null, offline: false, needs: 0, perPR: null, today: null, text: v.boss ? `${v.label} boss ${v.boss.status} · ${live} lanes` : `${v.label} no boss` };
    }
    const f = v.home;
    const live = f ? f.system?.agents?.live ?? f.live_agents ?? 0 : null;
    const needs = f ? (f.needs_you ?? []).filter((n) => (!n.org || v.orgs.has(n.org)) && !hidden(n.subject)).length : 0;
    const st = v.state;
    return {
      key: v.key, kind: v.kind, label: v.label, tag: f ? ROLE : null, offline: Boolean(v.error), needs,
      text: v.error && !f ? `${v.label} offline` : f ? `${v.label} ${ROLE} ${live} live` : `${v.label}…`,
      perPR: st.through?.cost?.cost_median != null ? `${money(st.through.cost.cost_median)}/PR` : null,
      today: st.tokens?.byDay?.[0]?.cost != null ? `${money(st.tokens.byDay[0].cost)} today` : null,
    };
  });
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
      if (text) cfg = merge(DEFAULTS, JSON.parse(text));
    } catch (e) {
      error = `dashboard.json: ${e?.message ?? e}`;
    }
    const before = data().config ? JSON.stringify(teamsOf(data().config)) : null;
    put({ config: cfg, configError: error });
    // A changed team address: drop what the old one said and ask the new one now.
    if (before !== null && before !== JSON.stringify(teamsOf(cfg))) put({ at: { config: Date.now() }, team: null });
    then?.(cx);
    cx.notify();
  });
}

// GET <team>/api/<path> as JSON, from the team's machine when one is configured.
async function teamGet(team, path, maxS) {
  const url = `${team.url.replace(/\/$/, "")}/api/${path}`;
  // curl, not fetch: a hard timeout when the team is off the network.
  const r = await exec("curl", ["-sS", "-m", String(maxS), "--connect-timeout", "4", url], { timeout: (maxS + 5) * 1000, on: team.machine || undefined });
  if (r.code !== 0) throw new Error(r.stderr.trim().replace(/^curl: \(\d+\)\s*/, "") || `curl exit ${r.code}`);
  return JSON.parse(r.stdout);
}

export function webUrl(path = "", team = teamsOf()[0]) {
  const base = team.web_url || (team.machine ? null : team.url) || null;
  return base ? `${base.replace(/\/$/, "")}${path}` : null;
}

function loadFactory(cx) {
  put({ at: stamp("factory") });
  for (const team of teamsOf())
    cx.spawn(async (cx) => {
      try {
        putTeam(team.key, { home: await teamGet(team, "home.json", 8), at: Date.now(), error: null, downSince: null });
      } catch (e) {
        putTeam(team.key, { error: String(e?.message ?? e), downSince: teamState(data(), team.key).downSince ?? Date.now() });
      }
      cx.notify();
    });
}

function loadJson(cx, stampKey, team, name, pick) {
  put({ at: stamp(stampKey) });
  cx.spawn(async (cx) => {
    try {
      putTeam(team.key, pick(await teamGet(team, `${name}.json`, 20)));
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

// The orgs to show: the config's list, or (auto) those the factory reports in use.
// One team's (by key), or every team's together.
export function orgsShown(d = data(), teamKey = null) {
  const want = config().orgs;
  if (Array.isArray(want)) return new Set(want);
  const out = new Set();
  for (const t of teamsOf()) {
    if (teamKey && t.key !== teamKey) continue;
    const lanes = teamState(d, t.key).home?.lanes ?? [];
    const used = lanes.filter((l) => l.boss?.status === "live" || l.director?.status === "live" || l.goals_active || l.goals_open || l.agents_live);
    for (const l of used.length ? used : lanes) out.add(l.org);
  }
  return out;
}

export function pipeline(github, d = data()) {
  const p = d.projects?.[github] ?? {};
  const prs = p.prs ?? [];
  const ready = prs.filter((x) => !x.draft && !x.hold);
  return { loaded: !!p.prs, error: p.error, prs, ready, drafts: prs.filter((x) => x.draft).length, merged: p.merged ?? [], lastMerge: (p.merged ?? [])[0]?.merged ?? null };
}

// The usage figures to show: this machine's file when configured, else the factory's.
export function usage(d = data()) {
  const f = teamsOf().map((t) => teamState(d, t.key)).map((s) => s.res?.usage ?? s.home?.system?.usage).find(Boolean);
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
  // An agent on another machine is named with that machine ("Mac mini · agent ...").
  const who = (s) => (s.machine != null ? `${labelFor(s.machine, d)} · ${s.name}` : s.name);
  for (const s of scan) if (s.login) out.push({ key: `login:${s.address ?? s.name}`, level: "danger", text: `${who(s)}: login expired, run /login`, agent: s });
  const u = usage(d);
  if (orgs.has("claude") && u.seven != null && u.seven >= A.usage_danger_pct)
    out.push({ key: `usage-danger`, level: "danger", text: `Claude weekly usage ${Math.round(u.seven)}% (cap ${u.capSeven}%)` });
  else if (orgs.has("claude") && u.seven != null && u.seven >= A.usage_warn_pct)
    out.push({ key: `usage-warn`, level: "warning", text: `Claude weekly usage ${Math.round(u.seven)}%` });
  for (const s of scan)
    for (const [k, w] of Object.entries(s.waits ?? {}))
      if (now - w.since >= w.after_min * 60_000)
        out.push({ key: `wait:${s.address ?? s.name}:${k}:${w.since}`, level: "danger", text: `${who(s)} ${w.label} · ${ago(w.since)}`, agent: s });
  for (const p of config().projects ?? []) {
    if (!p.github) continue;
    const { ready, lastMerge } = pipeline(p.github, d);
    const working = scan.filter((s) => s.project === p.name && s.status === "working").length;
    if (ready.length && lastMerge && now - lastMerge >= A.stall_minutes * 60_000)
      out.push({ key: `stall:${p.github}:${lastMerge}`, level: working ? "warning" : "danger", text: `${p.name ?? p.github}: no merge for ${ago(lastMerge)} · ${ready.length} PRs open · ${working} working` });
  }
  for (const s of scan)
    if (s.ctx != null && s.ctx < A.context_below_pct) out.push({ key: `ctx:${s.address ?? s.name}`, level: "warning", text: `${who(s)} context ${s.ctx}% left`, agent: s });
  const rows = machineRows(d);
  const probeDown = new Set(rows.filter((r) => !r.ok && !r.fromApi).map((r) => r.name));
  for (const r of rows) {
    if (!r.ok && !r.fromApi) out.push({ key: `machine-down:${r.key}`, level: "warning", text: `${r.label} unreachable` });
    if (r.disk_free_gb != null && r.disk_total_gb && (r.disk_free_gb < 20 || r.disk_free_gb / r.disk_total_gb < 0.05))
      out.push({ key: `disk:${r.key}:${new Date().toDateString()}`, level: r.disk_free_gb < 5 ? "danger" : "warning", text: `${r.label}: ${r.disk_free_gb} GB disk free` });
  }
  for (const team of teamsOf()) {
    const st = teamState(d, team.key);
    const label = teamLabel(team, d);
    const m = st.res?.machine;
    if (m?.swap_total_mb && m.swap_used_mb / m.swap_total_mb >= 0.95)
      out.push({
        key: `swap:${team.key}:${new Date().toDateString()}`, level: m.mem_available_mb < 2048 ? "danger" : "warning",
        text: `${label} swap full (${(m.swap_used_mb / 1024).toFixed(1)} GB) · ${(m.mem_available_mb / 1024).toFixed(1)} GB RAM free`,
      });
    // A team's machine already reported unreachable by the machine probe needs no second alert.
    if (st.downSince && now - st.downSince >= A.factory_down_minutes * 60_000 && !(team.machine && probeDown.has(team.machine)))
      out.push({ key: `team-down:${team.key}`, level: "warning", text: `${label} unreachable · ${ago(st.downSince)}` });
  }
  if (d.configError) out.push({ key: `config:${d.configError}`, level: "warning", text: d.configError });
  if (demo()) return config().demo?.alerts === false ? [] : out.filter((a) => !hidden(a.text)).map((a) => ({ ...a, text: clean(a.text) }));
  return out;
}

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

// The machines to show, in order: this computer, then hc machines; each team's machine carries the role tag, and
// comes from the team's own API when no hc machine row is that team's. -> [{key, label, role, ...facts}]
export function machineRows(d = data()) {
  const teams = teamsOf();
  // A team on this computer: no hc machine, and a loopback address.
  const ownsRow = (t, r) => (t.machine ? r.name === t.machine : r.name == null && isLoopback(t.url));
  const rows = Object.entries(d.machines ?? {}).map(([key, m]) => ({ key, ...m }));
  for (const r of rows) r.role = teams.some((t) => ownsRow(t, r)) ? ROLE : null;
  for (const t of teams) {
    const fm = teamState(d, t.key).res?.machine;
    if (rows.some((r) => ownsRow(t, r)) || !fm || !Object.keys(fm).length) continue;
    rows.push({
      key: teams.length > 1 ? `team-${t.key}` : "factory", name: t.machine, role: ROLE, ok: !teamState(d, t.key).error, os: null, cpus: fm.cpus ?? null,
      mem_total_mb: fm.mem_total_mb, mem_available_mb: fm.mem_available_mb, swap_total_mb: fm.swap_total_mb,
      swap_used_mb: fm.swap_used_mb, load: fm.load ?? [], uptime_s: null, fromApi: true, label: teamLabel(t, d),
    });
  }
  return rows.map((r) => ({ ...r, label: r.fromApi ? r.label : labelFor(r.name ?? null, d), agents: r.fromApi ? null : terminalsOn(r.name) }));
}

function terminalsOn(name) {
  return terminals().filter((t) => t.state === "live" && t.status && (name == null ? !String(t.address).includes("/") : String(t.address).startsWith(`${name}/`))).length;
}

export const warn = (t) => t.ansi3 ?? t.done;
export const good = (t) => t.ansi2 ?? t.working;
export const levelColor = (t, l) => ({ danger: t.blocked, warning: warn(t), info: t.accent }[l] ?? t.muted);
