// Tests for the Herdr app dashboard's data layer (mods/hivemill/data.js): factories on the obs API, their states,
// what needs you, alerts, machine labels, remote project spaces, demo mode.
// Run: node --test plugins/hivemill/herdr-app/tests/*.test.mjs
// The mod's "herdr" import is served by ./herdr-stub.mjs through a resolve hook; the mod files are not touched.
import { test, beforeEach, afterEach, mock } from "node:test";
import assert from "node:assert/strict";
import { register } from "node:module";
import { readFileSync } from "node:fs";

register("./hooks.mjs", import.meta.url);
const stub = await import("./herdr-stub.mjs");
const D = await import("../mods/hivemill/data.js");
const S = stub.S;

const fx = (name) => JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), "utf8"));
const T0 = Date.parse("2026-11-14T10:00:00Z");
const CFG_PATH = "/fixture/.config/hivemill/dashboard.json";
const BUSY = "http://192.0.2.10:7461", PILOT = "http://127.0.0.1:7461";
const KEY_BUSY = BUSY, KEY_PILOT = `macmini ${PILOT}`;

beforeEach(() => {
  stub.reset();
  mock.timers.enable({ apis: ["Date"], now: T0 });
});
afterEach(() => mock.timers.reset());

// Runs the mod's own collector (config, factories, scan, machines) to the end with this settings file.
async function collect(cfg) {
  S.files[CFG_PATH] = JSON.stringify(cfg);
  const pending = [];
  const cx = { spawn: (fn) => pending.push(Promise.resolve().then(() => fn(cx))), notify() {} };
  D.tick("test", cx, true);
  while (pending.length) await pending.shift();
  return D.data();
}

const serveBusy = () => {
  S.http[`local ${BUSY}/api/obs/now`] = fx("now-busy.json");
  S.http[`local ${BUSY}/api/obs/flow?days=14`] = fx("flow-busy.json");
  S.http[`local ${BUSY}/api/obs/flow?days=1`] = fx("flow-day.json");
  S.http[`local ${BUSY}/api/obs/machines`] = fx("machines-busy.json");
  S.http[`local ${BUSY}/api/obs/spend?days=7&by=task`] = fx("spend-busy.json");
  S.http[`local ${BUSY}/api/obs/task?k=${encodeURIComponent("box-a/T-0110")}`] = fx("task-held.json");
};
const servePilot = () => {
  S.http[`macmini ${PILOT}/api/obs/now`] = fx("now-pilot.json");
};
const standardSetup = () => {
  S.spaces = fx("spaces.json");
  S.agents = fx("agents.json");
  S.terminals = S.agents.map((a) => ({ address: a.address, name: a.tab_name, state: "live", status: a.status }));
  S.machines = [{ name: "buildbox" }, { name: "macmini" }];
  serveBusy();
  servePilot();
};
const MACHINES = { local: { name: null, ok: true }, buildbox: { name: "buildbox", ok: true }, macmini: { name: "macmini", ok: true } };
const set = (patch) => stub.setShared("hivemill.data", { ...D.data(), ...patch });
const view = (label) => D.factoryViews(D.data()).find((v) => v.label === label);

// ---- factories on the obs API ------------------------------------------------------------------------------------

test("one view per factory: working, waiting for CI, waiting to merge and held come from the run stages", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  assert.deepEqual(D.factoryViews(D.data()).map((v) => [v.label, v.state]), [["Workshop", "live"], ["Pilot", "live"]]);
  const w = view("Workshop");
  assert.deepEqual(w.counts, { working: 3, ci: 2, merge: 2, held: 2 });
  assert.equal(w.waiting, 6);
  assert.deepEqual(view("Pilot").counts, { working: 1, ci: 0, merge: 0, held: 0 });
  assert.deepEqual(w.projects, ["app"], "the total row and the no-project row are not projects");
});

test("needs you: open questions and held runs, a held run with its reason and PR from the task trace", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const w = view("Workshop");
  assert.equal(w.needsCount, 4, "2 questions + 2 held");
  assert.deepEqual(w.needs.map((n) => n.kind), ["question", "held", "held"]);
  assert.equal(w.needs[0].text, "2 open questions · app");
  const held = w.needs[1];
  assert.equal(held.text, "tier T3 is above T2; the owner merges");
  assert.equal(held.pr, "https://example.com/acme/app/pull/17");
  assert.equal(held.href, held.pr);
  // A held run whose trace did not answer keeps the Now page's words and links to its trace page.
  assert.equal(w.needs[2].text, "held for the owner");
  assert.equal(w.needs[2].href, `${BUSY}/task?k=${encodeURIComponent("box-a/T-0111")}`);
  assert.equal(view("Pilot").needsCount, 0);
});

test("task titles: the API's own, else the worker tab's name on one of the factory's machines", async () => {
  standardSetup();
  const now = fx("now-busy.json");
  now.page.running[0].title = "From the API";
  S.http[`local ${BUSY}/api/obs/now`] = now;
  S.spaces = [...S.spaces,
    { id: "buildbox/workers", name: "workers", machine: "buildbox", tabs: [{ id: "t1", name: "T-0101 · from a tab" }, { id: "t2", name: "T-0110 · Held task title" }, { id: "t3", name: "T-0106 · judge" }] },
    { id: "macmini/workers", name: "workers", machine: "macmini", tabs: [{ id: "t4", name: "T-0110 · another factory's task" }, { id: "t5", name: "T-0107 · only here" }, { id: "t6", name: "2" }] }];
  await collect(fx("dashboard.json"));
  const w = view("Workshop");
  const title = (task) => w.running.find((r) => r.task === task)?.title;
  assert.equal(title("T-0101"), "From the API");
  assert.equal(w.needs.find((n) => n.task === "T-0110").title, "Held task title", "the tab on the factory's own machine wins");
  assert.equal(title("T-0107"), "only here", "a single tab anywhere is taken");
  assert.equal(title("T-0106"), null, "a judge tab names no task");
  assert.equal(w.spend.top.find((x) => x.task === "T-0110").title, "Held task title");
  assert.deepEqual(Object.keys(D.tabTitles(S.spaces)).sort(), ["T-0101", "T-0107", "T-0110"]);
});

test("board: today's tasks in the column of where they are, per project, with why a card is not moving", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const w = view("Workshop");
  const b = D.boardOf(w);
  assert.deepEqual(Object.fromEntries(Object.entries(b).map(([k, v]) => [k, v.length])), { working: 3, ci: 2, merge: 2, held: 2, done: 4 });
  assert.deepEqual(b.merge.map((c) => [c.task, c.bad]), [["T-0106", true], ["T-0107", true]], "longest first, over the limit marked");
  assert.match(b.merge[0].note, /^to merge 2h 0m$/);
  assert.equal(b.held[0].note, "32m, tier T3 is above T2; the owner merges");
  assert.deepEqual(b.done.map((c) => c.note).sort(), ["failed: budget", "failed: stalled", "failed: stalled", "merged"]);
  assert.equal(D.boardOf(w, "none-such").working.length, 0);
  assert.equal(D.boardOf(w, "app").working.length, 3);
  assert.equal(D.boardOf(view("Pilot")).working.length, 1);
});

test("a task's trace is fetched on demand and kept small", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const pending = [];
  const cx = { spawn: (fn) => pending.push(Promise.resolve().then(() => fn(cx))), notify() {} };
  D.loadTrace(cx, D.factoriesOf()[0], "box-a/T-0110");
  assert.equal(view("Workshop").trace.loading, true);
  while (pending.length) await pending.shift();
  const tr = view("Workshop").trace;
  assert.deepEqual([tr.key, tr.task, tr.usd, tr.steps.length, tr.steps[1].kind], ["box-a/T-0110", "T-0110", 6.15, 2, "run.held"]);
  D.loadTrace(cx, D.factoriesOf()[0], "box-a/none");
  while (pending.length) await pending.shift();
  assert.ok(view("Workshop").trace.error);
});

test("a held run's trace is asked once, not on every poll", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  await collect(fx("dashboard.json"));
  const asked = S.execCalls.filter((c) => c.cmd === "curl" && c.args.at(-1).includes("task?k=box-a%2FT-0110"));
  assert.equal(asked.length, 1);
});

test("planner, merges, spend, stuck and failed runs", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const w = view("Workshop"), p = view("Pilot");
  assert.deepEqual([w.planner.on, p.planner.on], [true, false]);
  assert.deepEqual([w.merged.today, w.merged.total, w.merged.days, w.merged.perDay.length], [64, 78, 14, 14]);
  assert.deepEqual([w.spend.today, w.spend.week, w.spend.perPR, w.spend.hours], [40, 123, 5.6, 0.75]);
  assert.deepEqual(w.spend.top.map((x) => x.key), ["box-a/T-0101", "box-a/T-0110"], "most expensive first, rows without a task left out");
  assert.deepEqual(w.spend.daily.map((x) => x.usd), [35, 40]);
  assert.deepEqual(w.stuck.map((r) => [r.task, r.stage]), [["T-0106", "merge"], ["T-0107", "merge"]]);
  assert.equal(D.stuckLimit(w.stuckAfter), "1h");
  assert.equal(w.failed.n, 3);
  assert.deepEqual(w.failed.groups.map((g) => [g.reason, g.n]), [["stalled", 2], ["budget", 1]]);
  assert.equal(p.failed, null, "a factory whose Flow did not answer shows no failed count, not zero");
});

test("the task list of Flow is not kept in the shared state", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  assert.ok(!JSON.stringify(D.data().fac).includes('"tasks"'));
});

test("a factory's machines carry local labels; a factory reached through an hc machine is asked from there", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const w = view("Workshop");
  assert.deepEqual(w.machines.map((m) => [m.label, m.coordinator, m.down, m.running]), [["box-a", true, false, 2], ["Bench 2", false, true, 0]]);
  assert.equal(w.cap, 16);
  const calls = S.execCalls.filter((c) => c.cmd === "curl");
  assert.ok(calls.some((c) => c.on === "macmini" && c.args.at(-1) === `${PILOT}/api/obs/now`));
  assert.ok(calls.filter((c) => c.args.at(-1).startsWith(BUSY)).every((c) => c.on === null));
});

test("ages count on between answers", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  mock.timers.tick(60_000);
  const w = view("Workshop");
  assert.equal(w.needs[1].age_s, 1920 + 60);
  assert.equal(w.running.find((r) => r.task === "T-0101").age_s, 660);
});

// ---- states: nothing is shown as live after its source stopped answering ----------------------------------------------

test("a factory that never answered is down; one still loading is loading; neither has counts", async () => {
  standardSetup();
  delete S.http[`macmini ${PILOT}/api/obs/now`];
  await collect(fx("dashboard.json"));
  const p = view("Pilot");
  assert.deepEqual([p.state, p.loaded, p.counts], ["down", false, undefined]);
  assert.equal(view("Workshop").state, "live", "one factory down never blanks another");
  set({ fac: null });
  assert.deepEqual(D.factoryViews(D.data()).map((v) => v.state), ["loading", "loading"]);
  assert.deepEqual(D.barSegments(D.data()).map((s) => s.text), ["Workshop…", "Pilot…"]);
});

test("a factory that stops answering goes stale: last known counts, no need-you alerts, one unreachable alert after the wait", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  assert.ok(D.alerts(D.data()).some((a) => a.text.startsWith("Workshop: T-0110 held")));
  for (const k of Object.keys(S.http)) if (k.includes(BUSY)) delete S.http[k];
  mock.timers.tick(60_000);
  await collect(fx("dashboard.json"));
  let w = view("Workshop");
  assert.equal(w.state, "stale");
  assert.equal(w.counts.held, 2, "the last known numbers stay");
  let texts = D.alerts(D.data()).map((a) => a.text);
  assert.ok(!texts.some((x) => x.includes("held") || x.includes("question") || x.includes("unreachable")), texts.join("|"));
  assert.equal(D.barSegments(D.data())[0].needs, 0);
  assert.match(D.barSegments(D.data())[0].text, /^Workshop 3 working · 6 waiting · 1m old$/);
  mock.timers.tick(5 * 60_000);
  texts = D.alerts(D.data()).map((a) => a.text);
  assert.deepEqual(texts.filter((x) => x.includes("Workshop")), ["Workshop unreachable · 5m"]);
  // It answers again: live, and the down clock is gone.
  serveBusy();
  await collect(fx("dashboard.json"));
  w = view("Workshop");
  assert.deepEqual([w.state, w.downSince], ["live", null]);
});

test("an answer older than stale_minutes is stale even without an error (the collector itself stopped)", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  mock.timers.tick(4 * 60_000);
  assert.equal(view("Workshop").state, "stale");
});

test("a slow factory does not delay another: each factory has its own spawned fetches", async () => {
  standardSetup();
  S.files[CFG_PATH] = JSON.stringify(fx("dashboard.json"));
  const pending = [];
  const cx = { spawn: (fn) => pending.push(fn), notify() {} };
  D.tick("test", cx, true);
  await pending.shift()(cx); // the settings
  const before = pending.length;
  assert.ok(before >= 8, `two factories, four fetches each, got ${before}`);
});

// ---- bar and alerts ---------------------------------------------------------------------------------------------------

test("bar: '<label> N working · N waiting', the need-you count, and spend today over the factories", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const segs = D.barSegments(D.data());
  assert.deepEqual(segs.map((s) => s.text), ["Workshop 3 working · 6 waiting", "Pilot 1 working · 0 waiting"]);
  assert.deepEqual(segs.map((s) => s.needs), [4, 0]);
  assert.equal(D.spendToday(D.data()), 42.4);
});

test("alerts: questions, held runs with reason and age, a down factory machine, stuck runs as one line", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "", "macmini/t-w1": "" };
  await collect(fx("dashboard.json"));
  set({ machines: MACHINES });
  const list = D.alerts(D.data());
  assert.deepEqual(list.map((a) => a.text), [
    "Workshop: 2 open questions · app",
    "Workshop: T-0110 held · tier T3 is above T2; the owner merges · 32m",
    "Workshop: T-0111 held · held for the owner · 10m",
    "Workshop: machine Bench 2 is down",
    "Workshop: 2 runs over 1h in one stage · oldest T-0106, merge 2h 0m",
  ]);
  assert.deepEqual(list.map((a) => a.level), ["danger", "warning", "warning", "warning", "info"]);
  assert.equal(list[1].href, "https://example.com/acme/app/pull/17");
  assert.equal(new Set(list.map((a) => a.key)).size, list.length);
});

test("a long hold reason is cut in the alert, so a notification stays short; the card keeps all of it", async () => {
  standardSetup();
  const tr = fx("task-held.json");
  tr.steps[1].summary = `held: ${"judge HOLD: the owner decides. ".repeat(12)}`;
  S.http[`local ${BUSY}/api/obs/task?k=${encodeURIComponent("box-a/T-0110")}`] = tr;
  await collect({ ...fx("dashboard.json"), projects: [] });
  const a = D.alerts(D.data()).find((x) => x.text.includes("T-0110"));
  assert.ok(a.text.length < 130 && a.text.includes("… · 32m"), a.text);
  assert.ok(view("Workshop").needs[1].text.length > 300);
  assert.equal(D.brief("short"), "short");
});

test("Claude usage alerts come from the usage file, and not from one that is stale", async () => {
  standardSetup();
  S.files["/fixture/usage.json"] = JSON.stringify({ five_hour_pct: "12", seven_day_pct: "91", updated_at: T0 / 1000 });
  await collect({ ...fx("dashboard.json"), usage_file: "~/usage.json", projects: [] });
  assert.ok(D.alerts(D.data()).some((a) => a.text === "Claude weekly usage 91%" && a.level === "danger"));
  assert.deepEqual([D.usage().seven, D.usage().five, D.usage().stale], [91, 12, false]);
  S.files["/fixture/usage.json"] = JSON.stringify({ seven_day_pct: "91", updated_at: T0 / 1000 - 7200 });
  await collect({ ...fx("dashboard.json"), usage_file: "~/usage.json", projects: [] });
  assert.equal(D.usage().stale, true);
  assert.ok(!D.alerts(D.data()).some((a) => a.text.includes("usage")));
});

test("swap full and low disk on a probed machine are named by its label", async () => {
  standardSetup();
  await collect({ ...fx("dashboard.json"), projects: [] });
  set({ machines: { ...MACHINES, buildbox: { name: "buildbox", ok: true, swap_total_mb: 4096, swap_used_mb: 4000, mem_available_mb: 1024, disk_total_gb: 100, disk_free_gb: 4 } } });
  const texts = D.alerts(D.data()).map((a) => a.text);
  assert.ok(texts.includes("Lenovo: 4 GB disk free"), texts.join("|"));
  assert.ok(texts.includes("Lenovo swap full (3.9 GB) · 1.0 GB RAM free"), texts.join("|"));
});

// ---- settings ---------------------------------------------------------------------------------------------------------

test("no settings file: one factory on this computer's obs port", async () => {
  await collect({});
  const fs = D.factoriesOf();
  assert.deepEqual(fs.map((f) => [f.url, f.machine]), [["http://127.0.0.1:7461", null]]);
  assert.equal(D.factoryLabel(fs[0]), "This computer");
});

test("settings written for the old dashboard raise one settings alert and are not read", async () => {
  await collect({ teams: [{ machine: "buildbox", url: "http://127.0.0.1:1" }], factory: { url: "http://127.0.0.1:1" } });
  assert.deepEqual(D.factoriesOf().map((f) => f.url), ["http://127.0.0.1:7461"]);
  assert.ok(D.alerts(D.data()).some((a) => /list your factories under "factories"/.test(a.text)));
  assert.ok(!S.execCalls.some((c) => c.cmd === "curl" && c.args.at(-1).includes(":1/")));
});

test("factory labels: own label, else the hc machine's label, else Factory N; web links follow web_url", async () => {
  await collect({ factories: [{ url: "http://192.0.2.10:7461/" }, { url: PILOT, machine: "macmini", web_url: "http://192.0.2.20:7461" }, { url: PILOT, label: "Third" }], machine_labels: { macmini: "Mac mini" } });
  const fs = D.factoriesOf();
  assert.deepEqual(fs.map((f) => D.factoryLabel(f)), ["Factory 1", "Mac mini", "Third"]);
  assert.equal(new Set(fs.map((f) => f.key)).size, 3);
  assert.equal(D.webUrl("/flow", fs[0]), "http://192.0.2.10:7461/flow");
  assert.equal(D.webUrl("/", fs[1]), "http://192.0.2.20:7461/");
  assert.equal(D.webUrl("/", { ...fs[1], web_url: null }), null, "a loopback address on another machine is no link here");
  assert.equal(D.taskUrl(fs[0], "box-a/T-0001"), "http://192.0.2.10:7461/task?k=box-a%2FT-0001");
});

test("a changed factory address drops what the old one said", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  assert.ok(D.data().fac[KEY_BUSY].now);
  await collect({ ...fx("dashboard.json"), factories: [{ label: "Workshop", url: "http://192.0.2.99:7461" }] });
  assert.equal(D.data().fac?.[KEY_BUSY], undefined);
  assert.equal(view("Workshop").state, "down");
  assert.ok(KEY_PILOT);
});

test("a task's worker terminal is found only when exactly one is named for it", () => {
  const terms = [{ address: "a/1", name: "claude-task-t-0110", state: "live" }, { address: "a/2", name: "claude-judge-t-0111", state: "live" }, { address: "b/3", name: "claude-task-t-0111-x", state: "live" }, { address: "a/4", name: "planner", state: "live" }];
  assert.equal(D.workerTerminal("T-0110", terms), "a/1");
  assert.equal(D.workerTerminal("T-110", terms), "a/1");
  assert.equal(D.workerTerminal("T-0111", terms), null, "two terminals: ambiguous");
  assert.equal(D.workerTerminal("T-0999", terms), null);
  assert.equal(D.workerTerminal("", terms), null);
});

// ---- machines and spaces -------------------------------------------------------------------------------------------

test("machine labels: local first; the role tag on the machine a factory's planner space or hc link names", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  set({ machines: MACHINES });
  const rows = D.machineRows(D.data());
  assert.deepEqual(rows.map((r) => r.label), ["MacBook", "Lenovo", "Mac mini"]);
  assert.deepEqual(rows.map((r) => r.role), [null, "factory", "factory"]);
});

test("a machine without a label falls back to its hc name, local to This computer", async () => {
  standardSetup();
  await collect({ ...fx("dashboard.json"), machine_labels: { buildbox: "Lenovo" } });
  set({ machines: MACHINES });
  assert.deepEqual(D.machineRows(D.data()).map((r) => r.label), ["This computer", "Lenovo", "macmini"]);
});

test("resolveSpace: machine/space, local first, one remote match, none", () => {
  const sp = fx("spaces.json");
  assert.equal(D.resolveSpace("macmini/teamb", sp).id, "sp-d");
  assert.equal(D.resolveSpace("macmini/sp-d", sp).id, "sp-d");
  assert.equal(D.resolveSpace("buildbox/hivemill", sp).machine, "buildbox");
  assert.equal(D.resolveSpace("hivemill", sp).id, "s1");
  assert.equal(D.resolveSpace("teamb", sp).id, "sp-d", "no local match: the single remote one");
  assert.equal(D.resolveSpace("other/teamb", sp), null, "wrong machine");
  assert.equal(D.resolveSpace("nope", sp), null);
  assert.equal(D.resolveSpace(null, sp), null);
  const twice = [...sp, { id: "sp-d2", name: "teamb", machine: "box2" }];
  assert.equal(D.resolveSpace("teamb", twice), null, "two remote matches stay ambiguous");
  assert.equal(D.resolveSpace("teamb", [{ id: "l", name: "teamb", machine: null }, ...twice]).id, "l");
});

test("scan reads agents in a project's remote space: SIM GO, login and context alerts name Mac mini", async () => {
  standardSetup();
  S.screens = { "t-local": "all fine", "macmini/t-boss": "status ctx 5% left", "macmini/t-w1": "ready.\nSIM GO\n" };
  const cfg = { ...fx("dashboard.json"), factories: [{ label: "Pilot", url: PILOT, machine: "macmini" }] };
  await collect(cfg);
  const scan = D.data().scan;
  assert.deepEqual(Object.keys(scan).sort(), ["macmini/t-boss", "macmini/t-w1", "t-local"]);
  assert.equal(scan["macmini/t-w1"].machine, "macmini");
  assert.deepEqual(D.alerts(D.data()).map((a) => a.text), ["Mac mini · teamb-boss context 5% left"]);
  mock.timers.tick(2 * 60_000);
  await collect(cfg);
  mock.timers.tick(14 * 60_000);
  await collect(cfg);
  const texts = D.alerts(D.data()).map((a) => a.text);
  assert.ok(texts.includes("Mac mini · teamb-w1 waits for SIM GO · 16m"), texts.join("|"));
  S.screens["macmini/t-w1"] = "Please run /login";
  await collect(cfg);
  assert.ok(D.alerts(D.data()).some((a) => a.text === "Mac mini · teamb-w1: login expired, run /login"));
});

test("a remote read goes to the agent's address; unlisted remote spaces are never read; no project, no remote read", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "", "macmini/t-w1": "" };
  await collect(fx("dashboard.json"));
  const reads = S.hcCalls.filter((c) => c[0] === "read").map((c) => c[1]).sort();
  assert.deepEqual(reads, ["macmini/t-boss", "macmini/t-w1", "t-local"]);
  assert.deepEqual(S.hcCalls.find((c) => c[1] === "macmini/t-w1"), ["read", "macmini/t-w1", "--lines", "30"]);
  S.hcCalls.length = 0;
  await collect({ ...fx("dashboard.json"), projects: [] });
  assert.deepEqual(S.hcCalls.filter((c) => c[0] === "read").map((c) => c[1]), ["t-local"]);
});

test("project space on a machine that is offline: no crash and no alert from it", async () => {
  standardSetup();
  S.spaces = S.spaces.filter((s) => s.machine == null);
  S.agents = S.agents.filter((a) => !a.address.startsWith("macmini/"));
  S.screens = { "t-local": "" };
  await collect({ ...fx("dashboard.json"), factories: [{ label: "Pilot", url: PILOT, machine: "macmini" }] });
  assert.deepEqual(D.alerts(D.data()), []);
});

// ---- demo mode --------------------------------------------------------------------------------------------------

function everyString(d) {
  const out = [];
  const add = (x) => typeof x === "string" && out.push(x);
  D.machineRows(d).forEach((r) => add(r.label));
  D.factoryViews(d).forEach((v) => (add(v.label), (v.machines ?? []).forEach((m) => add(m.label))));
  D.barSegments(d).forEach((s) => add(s.text));
  D.alerts(d).forEach((a) => add(a.text));
  add(D.locationLabel({ machine: "macmini", branch: "factory/t-1" }, d));
  return out;
}

test("demo mode: generic factory and machine names, no task id, hc name or factory label in any string", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "ctx 5% left", "macmini/t-w1": "" };
  await collect({ ...fx("dashboard.json"), machine_labels: {}, demo: { enabled: true, hide: [], alerts: true } });
  set({ machines: MACHINES });
  const d = D.data();
  assert.deepEqual(D.factoryViews(d).map((v) => v.label), ["Factory 1", "Machine 2"], "a factory on an hc machine is named as that machine");
  assert.deepEqual(D.machineRows(d).map((r) => r.label), ["This computer", "Machine 1", "Machine 2"]);
  const strings = everyString(d);
  assert.ok(strings.length > 10);
  for (const s of strings) assert.doesNotMatch(s, /buildbox|macmini|box-[ab]|Workshop|Pilot|\w\/\w|T-\d|\bG-\d/i, s);
  assert.ok(strings.includes("Factory 1: held · tier T3 is above T2; the owner merges · 32m"), strings.join("|"));
  assert.ok(strings.includes("Factory 1: 2 runs over 1h in one stage · oldest merge 2h 0m"), strings.join("|"));
});

test("demo mode: machine_labels values are shown as is; a hide word falls back to the generic name; alerts can be off", async () => {
  standardSetup();
  await collect({ ...fx("dashboard.json"), demo: { enabled: true, hide: ["lenovo"], alerts: true } });
  set({ machines: MACHINES });
  assert.deepEqual(D.machineRows(D.data()).map((r) => r.label), ["MacBook", "Machine 1", "Mac mini"]);
  await collect({ ...fx("dashboard.json"), demo: { enabled: true, hide: [], alerts: false } });
  assert.deepEqual(D.alerts(D.data()), []);
});

// ---- the shipped files ------------------------------------------------------------------------------------------------

test("the shipped mod has nothing of the old chain: no old API, roles, gate or port", () => {
  for (const f of ["main.js", "data.js"]) {
    const src = readFileSync(new URL(`../mods/hivemill/${f}`, import.meta.url), "utf8");
    assert.doesNotMatch(src, /spawn gate|leads|director|\bboss\b|lanes|live \/ cap|:8765|home\.json|resources\.json|throughput|orgs/i, f);
    assert.doesNotMatch(src, /["'`]Factory["'`]/, `${f}: no hard-coded Factory title`);
  }
});

test("the example settings file declares factories on the obs port, machine_labels and a project space", () => {
  const ex = JSON.parse(readFileSync(new URL("../dashboard.example.json", import.meta.url), "utf8"));
  assert.ok(Array.isArray(ex.factories) && ex.factories.length === 2);
  assert.ok(ex.factories.every((f) => /:7461$/.test(f.url) && f.label));
  assert.ok(ex.machine_labels["local"]);
  assert.ok(ex.projects[0].space && ex.projects[0].wait_patterns.length);
  assert.deepEqual(Object.keys(ex).sort(), Object.keys(D.DEFAULTS).sort());
});

// ---- one read per render ------------------------------------------------------------------------------------------------

test("oneRead: data() reuses one copy inside a render, reads fresh outside it, and sees its own put", () => {
  stub.setShared("hivemill.data", { mark: 1 });
  let reads = 0;
  const real = stub.S.shared;
  stub.S.shared = new Proxy(real, { get: (o, k) => (k === "hivemill.data" && reads++, o[k]) });
  const seen = D.oneRead(() => {
    const a = D.data(), b = D.data();
    D.config(); D.demo(); D.hidden("x"); D.clean("T-0001: y");
    real["hivemill.data"] = { mark: 2 }; // another copy's write lands mid-render
    return [a.mark, b.mark, D.data().mark, D.oneRead(() => D.data().mark)];
  });
  assert.deepEqual(seen, [1, 1, 1, 1]);
  assert.equal(reads, 1);
  assert.equal(D.data().mark, 2);
  stub.S.shared = real;
});
