// Tests for the Herdr app dashboard's data layer (mods/hivemill/data.js): machine labels, teams, project bosses,
// remote project spaces, demo mode. Run: node --test plugins/hivemill/herdr-app/tests/
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
const URL_DEFAULT = "http://127.0.0.1:8765";

beforeEach(() => {
  stub.reset();
  mock.timers.enable({ apis: ["Date"], now: T0 });
});
afterEach(() => mock.timers.reset());

// Runs the mod's own collector (config, teams, scan, machines) to the end with this settings file.
async function collect(cfg) {
  S.files[CFG_PATH] = JSON.stringify(cfg);
  const pending = [];
  const cx = { spawn: (fn) => pending.push(Promise.resolve().then(() => fn(cx))), notify() {} };
  D.tick("test", cx, true);
  while (pending.length) await pending.shift();
  return D.data();
}

const standardSetup = () => {
  S.spaces = fx("spaces.json");
  S.agents = fx("agents.json");
  S.terminals = S.agents.map((a) => ({ address: a.address, name: a.tab_name, state: "live", status: a.status }));
  S.machines = [{ name: "buildbox" }, { name: "macmini" }];
  S.http[`buildbox ${URL_DEFAULT}/api/home.json`] = fx("home-lenovo.json");
  S.http[`buildbox ${URL_DEFAULT}/api/resources.json`] = fx("resources.json");
};
const MACHINES = { local: { name: null, ok: true }, buildbox: { name: "buildbox", ok: true }, macmini: { name: "macmini", ok: true } };
const set = (patch) => stub.setShared("hivemill.data", { ...D.data(), ...patch });

// ---- (1) naming ---------------------------------------------------------------------------------------------------

test("machine labels: MacBook, Lenovo, Mac mini, local first; the role tag only on the team's machine", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  set({ machines: MACHINES });
  const rows = D.machineRows(D.data());
  assert.deepEqual(rows.map((r) => r.label), ["MacBook", "Lenovo", "Mac mini"]);
  assert.deepEqual(rows.map((r) => r.role), [null, "factory", null]);
});

test("a machine without a label falls back to its hc name, local to This computer", async () => {
  standardSetup();
  await collect({ ...fx("dashboard.json"), machine_labels: { buildbox: "Lenovo" } });
  set({ machines: MACHINES });
  assert.deepEqual(D.machineRows(D.data()).map((r) => r.label), ["This computer", "Lenovo", "macmini"]);
});

test("no name, title, bar segment or alert the code makes says factory (the role tag aside)", async () => {
  standardSetup();
  S.http[`buildbox ${URL_DEFAULT}/api/home.json`] = fx("home-lenovo.json");
  await collect(fx("dashboard.json"));
  set({ machines: MACHINES });
  // A swap-full alert, a down team and agent alerts: every kind of text with a team's name in it.
  S.screens["macmini/t-w1"] = "SIM GO";
  S.screens["macmini/t-boss"] = "Please run /login";
  await collect(fx("dashboard.json"));
  Date.now(); // mock clock
  mock.timers.tick(20 * 60_000);
  const d = D.data();
  const texts = [
    ...D.machineRows(d).map((r) => r.label),
    ...D.teamViews(d).map((v) => v.label),
    ...D.barSegments(d).map((s) => s.text.replace(s.tag ?? "\u0000", "")),
    ...D.alerts(d).map((a) => a.text),
    D.locationLabel({ machine: null }, d),
  ];
  assert.ok(D.alerts(d).length >= 2, "the fixture must raise alerts");
  for (const x of texts) assert.doesNotMatch(x, /factory/i, x);
});

test("the shipped mod source has no hard-coded Factory title", () => {
  for (const f of ["data.js", "main.js"]) {
    const src = readFileSync(new URL(`../mods/hivemill/${f}`, import.meta.url), "utf8");
    assert.doesNotMatch(src, /["'`]Factory/, f);
  }
});

test("a swap-full and a down team alert are named by the team's machine label", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const dd = D.data();
  const res = { ...dd.team.buildbox.res, machine: { ...dd.team.buildbox.res.machine, swap_used_mb: 4000 } };
  set({ team: { ...dd.team, buildbox: { ...dd.team.buildbox, res, downSince: T0 } } });
  mock.timers.tick(6 * 60_000);
  const texts = D.alerts(D.data()).map((a) => a.text);
  assert.ok(texts.some((x) => x.startsWith("Lenovo swap full")), texts.join("|"));
  assert.ok(texts.some((x) => x.startsWith("Lenovo unreachable · 6m")), texts.join("|"));
});

// ---- status bar -------------------------------------------------------------------------------------------------

test("bar: a live team reads '<label> factory N live'; a down team '<label> offline'; loading '<label>…'", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const [seg] = D.barSegments(D.data());
  assert.equal(seg.text, "Lenovo factory 3 live");
  assert.equal(seg.needs, 1);
  const dd = D.data();
  set({ team: { buildbox: { ...dd.team.buildbox, home: null, error: "refused" } } });
  assert.equal(D.barSegments(D.data())[0].text, "Lenovo offline");
  set({ team: {} });
  assert.equal(D.barSegments(D.data())[0].text, "Lenovo…");
});

test("bar: two teams make two segments, the leftmost label uses machine_labels", async () => {
  standardSetup();
  S.http[`macmini ${URL_DEFAULT}/api/home.json`] = fx("home-second.json");
  const cfg = { ...fx("dashboard.json"), teams: [{ machine: "buildbox" }, { machine: "macmini" }], projects: [] };
  await collect(cfg);
  assert.deepEqual(D.barSegments(D.data()).map((s) => s.text), ["Lenovo factory 3 live", "Mac mini factory 5 live"]);
  assert.equal(D.locationLabel({ machine: null }, D.data()), "MacBook");
  assert.equal(D.locationLabel({ machine: "macmini" }, D.data()), "Mac mini");
});

// ---- (2) more than one team / boss -------------------------------------------------------------------------------------

test("two teams: each has its own boss and lanes; one erroring leaves the other intact, one alert after the wait", async () => {
  standardSetup();
  S.http[`macmini ${URL_DEFAULT}/api/home.json`] = new Error("down"); // team 2 unreachable
  const cfg = { ...fx("dashboard.json"), teams: [{ machine: "buildbox" }, { machine: "macmini" }], projects: [] };
  await collect(cfg);
  let views = D.teamViews(D.data());
  assert.deepEqual(views.map((v) => v.label), ["Lenovo", "Mac mini"]);
  assert.equal(views[0].lanes[0].boss, "live");
  assert.equal(views[0].lanes[0].director, "live");
  assert.match(views[0].lanes[0].text, /boss live · director live · 2 workers/);
  assert.ok(views[0].home && !views[0].error);
  assert.ok(!views[1].home && views[1].error);
  // Before factory_down_minutes: no alert. After: exactly one, named by the label.
  assert.equal(D.alerts(D.data()).filter((a) => /unreachable/.test(a.text)).length, 0);
  mock.timers.tick(6 * 60_000);
  const down = D.alerts(D.data()).filter((a) => /unreachable/.test(a.text));
  assert.equal(down.length, 1);
  assert.match(down[0].text, /^Mac mini unreachable · 6m$/);
  // Team 2 coming back clears its error without touching team 1.
  S.http[`macmini ${URL_DEFAULT}/api/home.json`] = fx("home-second.json");
  await collect(cfg);
  views = D.teamViews(D.data());
  assert.ok(views[1].home && !views[1].error);
  assert.equal(views[0].lanes[0].boss, "live");
  assert.equal(D.alerts(D.data()).filter((a) => /unreachable/.test(a.text)).length, 0);
});

test("a team whose machine the probe already reports unreachable gets no second alert", async () => {
  standardSetup();
  S.http[`buildbox ${URL_DEFAULT}/api/home.json`] = new Error("down");
  await collect(fx("dashboard.json"));
  set({ machines: { ...MACHINES, buildbox: { name: "buildbox", ok: false } } });
  mock.timers.tick(6 * 60_000);
  assert.equal(D.alerts(D.data()).filter((a) => a.text.startsWith("Lenovo unreachable")).length, 1);
});

test("a slow team does not delay another: each team is its own spawned fetch", async () => {
  standardSetup();
  S.http[`macmini ${URL_DEFAULT}/api/home.json`] = fx("home-second.json");
  const cfg = { ...fx("dashboard.json"), teams: [{ machine: "buildbox" }, { machine: "macmini" }], projects: [] };
  S.files[CFG_PATH] = JSON.stringify(cfg);
  const spawned = [];
  const cx = { spawn: (fn) => spawned.push(Promise.resolve().then(() => fn(cx))), notify() {} };
  D.tick("test", cx, true);
  await spawned.shift(); // config
  const before = spawned.length;
  assert.ok(before >= 4, `home, usage, scan, machines, per-team fetches: ${before}`);
  while (spawned.length) await spawned.shift();
  const homeCalls = S.execCalls.filter((c) => c.cmd === "curl" && c.args.at(-1).endsWith("/home.json"));
  assert.deepEqual(homeCalls.map((c) => c.on).sort(), ["buildbox", "macmini"]);
});

test("project boss, no API: the boss's status and the project's agents as lanes, on the project's machine", async () => {
  standardSetup();
  await collect(fx("dashboard.json"));
  const views = D.teamViews(D.data());
  const dib = views.find((v) => v.kind === "project");
  assert.equal(dib.label, "Mac mini");
  assert.equal(dib.machine, "macmini");
  assert.equal(dib.boss.name, "teamb-boss");
  assert.equal(dib.boss.status, "working");
  assert.deepEqual(dib.lanes.map((l) => [l.name, l.status]), [["teamb-w1", "idle"]]);
  assert.equal(D.barSegments(D.data()).find((s) => s.kind === "project").text, "Mac mini boss working · 1 lanes");
});

test("project boss given as a regex finds it too; an unknown boss shows no boss", async () => {
  standardSetup();
  const p = fx("dashboard.json").projects[0];
  await collect({ ...fx("dashboard.json"), projects: [{ ...p, boss: { match: "^teamb-b" } }] });
  assert.equal(D.teamViews(D.data()).find((v) => v.kind === "project").boss.name, "teamb-boss");
  await collect({ ...fx("dashboard.json"), projects: [{ ...p, boss: "nobody" }] });
  const v = D.teamViews(D.data()).find((x) => x.kind === "project");
  assert.equal(v.boss, null);
  assert.equal(D.barSegments(D.data()).find((s) => s.kind === "project").text, "Mac mini no boss");
});

// ---- (3) remote project spaces -------------------------------------------------------------------------------------

test("resolveSpace: machine/space, local first, one remote match, none", () => {
  const sp = fx("spaces.json");
  assert.equal(D.resolveSpace("macmini/teamb", sp).id, "sp-d");
  assert.equal(D.resolveSpace("macmini/sp-d", sp).id, "sp-d");
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
  S.screens = {
    "t-local": "all fine",
    "macmini/t-boss": "status ctx 5% left",
    "macmini/t-w1": "ready.\nSIM GO\n",
  };
  await collect(fx("dashboard.json"));
  const scan = D.data().scan;
  assert.deepEqual(Object.keys(scan).sort(), ["macmini/t-boss", "macmini/t-w1", "t-local"]);
  assert.equal(scan["macmini/t-w1"].machine, "macmini");
  // Before the wait time: only the context alert; after: the SIM GO wait.
  assert.deepEqual(D.alerts(D.data()).map((a) => a.text), ["Mac mini · teamb-boss context 5% left"]);
  mock.timers.tick(16 * 60_000);
  const texts = D.alerts(D.data()).map((a) => a.text);
  assert.ok(texts.includes("Mac mini · teamb-w1 waits for SIM GO · 16m"), texts.join("|"));
  // A login-expired screen.
  S.screens["macmini/t-w1"] = "Please run /login";
  await collect(fx("dashboard.json"));
  assert.ok(D.alerts(D.data()).some((a) => a.text === "Mac mini · teamb-w1: login expired, run /login"));
});

test("a remote read goes to the agent's address, which names its machine; unlisted remote spaces are never read", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "", "macmini/t-w1": "" };
  await collect(fx("dashboard.json"));
  const reads = S.hcCalls.filter((c) => c[0] === "read").map((c) => c[1]).sort();
  assert.deepEqual(reads, ["macmini/t-boss", "macmini/t-w1", "t-local"]);
  assert.ok(!reads.includes("macmini/t-x"), "agent in a space no project names");
  assert.deepEqual(S.hcCalls.find((c) => c[1] === "macmini/t-w1"), ["read", "macmini/t-w1", "--lines", "30"]);
});

test("without any project, remote agents are not read at all (this computer's still are)", async () => {
  standardSetup();
  S.screens = { "t-local": "" };
  await collect({ ...fx("dashboard.json"), projects: [] });
  assert.deepEqual(S.hcCalls.filter((c) => c[0] === "read").map((c) => c[1]), ["t-local"]);
});

test("an unprefixed project space with no local match scans the single remote space of that name", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "", "macmini/t-w1": "SIM GO" };
  const p = fx("dashboard.json").projects[0];
  await collect({ ...fx("dashboard.json"), projects: [{ ...p, space: "teamb" }] });
  assert.ok(D.data().scan["macmini/t-w1"].waits["SIM GO"]);
});

test("project space on a machine that is offline: no crash, no alerts, no project team, no machine row", async () => {
  standardSetup();
  S.spaces = S.spaces.filter((s) => s.machine == null);
  S.agents = S.agents.filter((a) => !a.address.startsWith("macmini/"));
  S.screens = { "t-local": "" };
  await collect(fx("dashboard.json"));
  assert.deepEqual(D.alerts(D.data()), []);
  assert.deepEqual(D.teamViews(D.data()).map((v) => v.kind), ["hivemill"]);
});

// ---- demo mode --------------------------------------------------------------------------------------------------

function everyString(d) {
  const out = [];
  const add = (x) => typeof x === "string" && out.push(x);
  D.machineRows(d).forEach((r) => add(r.label)); // the role tag is a tag, not a name
  D.teamViews(d).forEach((v) => (add(v.label), (v.lanes ?? []).forEach((l) => (add(l.name), add(l.text))), add(v.boss?.name)));
  D.barSegments(d).forEach((s) => add(s.tag ? s.text.replace(s.tag, "") : s.text));
  D.alerts(d).forEach((a) => add(a.text));
  add(D.locationLabel({ machine: "macmini", branch: "factory/t-1" }, d));
  return out;
}

test("demo mode shows only machine_labels values: no hc name, space prefix, branch or task id", async () => {
  standardSetup();
  S.agents = S.agents.map((a) => (a.address === "macmini/t-w1" ? { ...a, tab_name: "claude-coder-t-1439" } : a));
  S.terminals = S.agents.map((a) => ({ address: a.address, name: a.tab_name, state: "live", status: a.status }));
  S.screens = { "t-local": "", "macmini/t-boss": "ctx 5% left", "macmini/t-w1": "SIM GO" };
  await collect({ ...fx("dashboard.json"), demo: { enabled: true, hide: [], alerts: true } });
  set({ machines: MACHINES });
  mock.timers.tick(16 * 60_000);
  const d = D.data();
  assert.deepEqual(D.machineRows(d).map((r) => r.label), ["MacBook", "Lenovo", "Mac mini"]);
  const strings = everyString(d);
  assert.ok(strings.length > 8);
  for (const s of strings) {
    assert.doesNotMatch(s, /buildbox|macmini|\w\/\w|T-\d|t-\d{3,}|\bG-\d/i, s);
  }
  assert.ok(strings.includes("Mac mini · teamb-boss context 5% left"));
});

test("demo mode without machine_labels: This computer and Machine N", async () => {
  standardSetup();
  S.screens = { "t-local": "", "macmini/t-boss": "ctx 5% left", "macmini/t-w1": "" };
  await collect({ ...fx("dashboard.json"), machine_labels: {}, demo: { enabled: true, hide: [], alerts: true } });
  set({ machines: MACHINES });
  const d = D.data();
  assert.deepEqual(D.machineRows(d).map((r) => r.label), ["This computer", "Machine 1", "Machine 2"]);
  for (const s of everyString(d)) assert.doesNotMatch(s, /buildbox|macmini|factory/i, s);
  assert.ok(D.alerts(d).some((a) => a.text === "Machine 2 · teamb-boss context 5% left"), D.alerts(d).map((a) => a.text).join("|"));
});

test("demo mode: a hide word in a label falls back to the generic name", async () => {
  standardSetup();
  await collect({ ...fx("dashboard.json"), demo: { enabled: true, hide: ["lenovo"], alerts: true } });
  set({ machines: MACHINES });
  assert.deepEqual(D.machineRows(D.data()).map((r) => r.label), ["MacBook", "Machine 1", "Mac mini"]);
});

// ---- backward compatibility ---------------------------------------------------------------------------------------

test("old config with only factory: one team, the same cards and data keys as before, no word factory in names", async () => {
  S.http[`${"local"} ${URL_DEFAULT}/api/home.json`] = fx("home-lenovo.json");
  S.http[`local ${URL_DEFAULT}/api/resources.json`] = fx("resources.json");
  S.machines = [];
  await collect({ factory: { url: URL_DEFAULT, machine: null, web_url: null }, projects: [] });
  const d = D.data();
  assert.deepEqual(D.teamsOf().map((t) => [t.key, t.machine]), [["local", null]]);
  const views = D.teamViews(d);
  assert.equal(views.length, 1);
  assert.equal(views[0].label, "This computer");
  assert.deepEqual(Object.keys(d.team["local"]).sort(), ["at", "downSince", "error", "home", "res"]);
  assert.deepEqual(Object.keys(d.team["local"].res).sort(), ["cursor", "machine", "rows", "usage"]);
  assert.equal(D.barSegments(d)[0].text, "This computer factory 3 live");
  assert.equal(D.webUrl("/x"), `${URL_DEFAULT}/x`);
  // The factory is on this computer: its own row carries the tag, no second row is invented.
  set({ machines: { local: { name: null, ok: true } } });
  assert.deepEqual(D.machineRows(D.data()).map((r) => [r.label, r.role]), [["This computer", "factory"]]);
});

test("old config with factory.machine and no labels: the hc name, never Factory", async () => {
  standardSetup();
  await collect({ factory: { url: URL_DEFAULT, machine: "buildbox", web_url: null }, projects: [] });
  assert.equal(D.barSegments(D.data())[0].text, "buildbox factory 3 live");
  set({ machines: MACHINES });
  assert.deepEqual(D.machineRows(D.data()).map((r) => r.label), ["This computer", "buildbox", "macmini"]);
});

test("a team reached by a network URL, with no hc machine, needs a label; without one it is just Server", async () => {
  const url = "http://lan-host:8765";
  S.http[`local ${url}/api/home.json`] = fx("home-lenovo.json");
  S.http[`local ${url}/api/resources.json`] = fx("resources.json");
  await collect({ factory: { url, machine: null, web_url: null }, projects: [] });
  assert.equal(D.barSegments(D.data())[0].text, "Server factory 3 live");
  await collect({ teams: [{ url, label: "Studio" }], projects: [] });
  assert.equal(D.barSegments(D.data())[0].text, "Studio factory 3 live");
  const rows = D.machineRows(D.data());
  assert.deepEqual(rows.map((r) => [r.label, r.role, r.fromApi ?? false]), [["This computer", null, false], ["Studio", "factory", true]]);
});

test("teams config wins over factory; the web url comes from the team", async () => {
  await collect({ factory: { url: "http://old:1" }, teams: [{ machine: "buildbox", web_url: "http://web:9/" }] });
  assert.deepEqual(D.teamsOf().map((t) => t.machine), ["buildbox"]);
  assert.equal(D.webUrl("/a", D.teamsOf()[0]), "http://web:9/a");
});

test("per-team figures: orgs, spend and usage come from the team that reported them", async () => {
  standardSetup();
  S.http[`macmini ${URL_DEFAULT}/api/home.json`] = fx("home-second.json");
  await collect({ ...fx("dashboard.json"), teams: [{ machine: "buildbox" }, { machine: "macmini" }], projects: [] });
  const d = D.data();
  assert.deepEqual([...D.orgsShown(d, "buildbox")], ["claude"]);
  assert.deepEqual([...D.orgsShown(d, "macmini")], ["cursor"]);
  assert.deepEqual([...D.orgsShown(d)].sort(), ["claude", "cursor"]);
  assert.equal(D.usage(d).seven, 50);
});

test("the example settings file declares teams, machine_labels and a remote project with a boss", () => {
  const ex = JSON.parse(readFileSync(new URL("../dashboard.example.json", import.meta.url), "utf8"));
  assert.ok(Array.isArray(ex.teams) && ex.teams[0].machine);
  assert.ok(ex.machine_labels["local"] && ex.machine_labels.macmini);
  const p = ex.projects.find((x) => x.space === "macmini/teamb");
  assert.ok(p?.boss);
});
