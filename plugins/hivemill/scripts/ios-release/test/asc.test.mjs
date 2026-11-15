import { test, before, after, beforeEach } from "node:test";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { AscError, AUTH_MESSAGE, createClient, resolveBaseUrl, signJwt } from "../asc.mjs";
import { makeFakeKey } from "./helpers/fakekey.mjs";
import { fakeClock, groupBody, startMock } from "./helpers/mock-asc.mjs";

const ASC = fileURLToPath(new URL("../asc.mjs", import.meta.url));
const key = makeFakeKey();
let mock;
let clock;

before(async () => { mock = await startMock(); });
after(async () => { await mock.close(); });
beforeEach(() => { mock.requests.length = 0; clock = fakeClock(); });

const client = (over = {}) => createClient({ keyId: key.keyId, issuerId: key.issuerId, keyPem: key.pem, baseUrl: mock.baseUrl, now: clock.now, sleep: clock.sleep, ...over });
const rejectsWith = async (p, code, re) => {
  await assert.rejects(p, (e) => { assert.ok(e instanceof AscError, String(e)); assert.equal(e.code, code); if (re) assert.match(e.message, re); return true; });
};
const decode = (s) => JSON.parse(Buffer.from(s, "base64url").toString());
const build = (id, version) => ({ id, type: "builds", attributes: { version } });

test("JWT has ES256 header, required claims, exp-iat <= 1200 and a verifiable 64-byte ieee-p1363 signature", () => {
  const token = signJwt(key.keyId, key.issuerId, key.pem, 1_700_000_000);
  const [h, p, s] = token.split(".");
  assert.deepEqual(decode(h), { alg: "ES256", kid: key.keyId, typ: "JWT" });
  const payload = decode(p);
  assert.equal(payload.iss, key.issuerId);
  assert.equal(payload.aud, "appstoreconnect-v1");
  assert.equal(payload.iat, 1_700_000_000);
  assert.ok(payload.exp - payload.iat <= 1200);
  const sig = Buffer.from(s, "base64url");
  assert.equal(sig.length, 64);
  assert.ok(crypto.verify("sha256", Buffer.from(`${h}.${p}`), { key: key.publicKey, dsaEncoding: "ieee-p1363" }, sig));
  assert.ok(!crypto.verify("sha256", Buffer.from(`${h}.x${p}`), { key: key.publicKey, dsaEncoding: "ieee-p1363" }, sig));
});

test("token is re-minted once fewer than 60 s remain, not before", async () => {
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [{ id: "A1" }] } }]);
  const c = client();
  await c.appId("com.example.a");
  clock.t += 1139 * 1000; // 61 s left
  await c.appId("com.example.a");
  clock.t += 1000; // 60 s left
  await c.appId("com.example.a");
  const [a, b, d] = mock.requests.map((r) => r.authorization);
  assert.equal(a, b);
  assert.notEqual(a, d);
  assert.equal(decode(d.split(".")[1]).iat, Math.floor(clock.t / 1000));
});

test("appId sends the bundle filter with a Bearer token; none found -> 6", async () => {
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [{ id: "A1" }] } }]);
  assert.equal(await client().appId("com.example.a"), "A1");
  assert.equal(mock.requests[0].query["filter[bundleId]"], "com.example.a");
  assert.match(mock.requests[0].authorization, /^Bearer eyJ/);
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [] } }]);
  await rejectsWith(client().appId("com.example.a"), 6);
});

test("next build is the numeric max + 1 across two pages (10 beats 9)", async () => {
  const page2 = `${mock.baseUrl}/v1/builds?cursor=2`;
  mock.on("GET", "/v1/builds", [
    { status: 200, body: (url) => (url.searchParams.get("cursor") ? { data: [build("b3", "10")] } : { data: [build("b1", "9"), build("b2", "2")], links: { next: page2 } }) },
  ]);
  assert.equal(await client().nextBuildNumber("A1", "1.2"), 11);
  assert.equal(mock.requests.length, 2);
  assert.equal(mock.requests[0].query["filter[app]"], "A1");
  assert.equal(mock.requests[0].query["filter[preReleaseVersion.version]"], "1.2");
  assert.equal(mock.requests[0].query.limit, "200");
});

test("next build is 1 when there are no builds", async () => {
  mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [] } }]);
  assert.equal(await client().nextBuildNumber("A1", "1.2"), 1);
});

test("a non-integer existing build number -> 6", async () => {
  mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [build("b1", "1.5")] } }]);
  await rejectsWith(client().nextBuildNumber("A1", "1.2"), 6, /not an integer/);
});

test("a next link to another host is refused (no JWT sent there)", async () => {
  mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [], links: { next: "https://evil.example/v1/builds?c=2" } } }]);
  await rejectsWith(client().nextBuildNumber("A1", "1.2"), 6, /different host/);
});

test("waitValid polls PROCESSING, not-yet-listed, PROCESSING, then VALID", async () => {
  const st = (s) => ({ status: 200, body: { data: [{ id: "B7", attributes: { processingState: s } }] } });
  mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [] } }, st("PROCESSING"), st("PROCESSING"), st("VALID")]);
  assert.equal(await client().waitValid("A1", "1.2", 5, 10, 30), "B7");
  assert.equal(mock.requests.length, 4);
  assert.deepEqual(clock.sleeps, [30, 30, 30]);
  assert.equal(mock.requests[0].query["filter[version]"], "5");
  assert.equal(mock.requests[0].query["filter[preReleaseVersion.version]"], "1.2");
});

test("waitValid: INVALID and FAILED -> 8", async () => {
  for (const state of ["INVALID", "FAILED"]) {
    mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [{ id: "B7", attributes: { processingState: state } }] } }]);
    await rejectsWith(client().waitValid("A1", "1.2", 5, 10, 30), 8, new RegExp(state));
  }
});

test("waitValid: timeout on the fake clock -> 8", async () => {
  mock.on("GET", "/v1/builds", [{ status: 200, body: { data: [{ id: "B7", attributes: { processingState: "PROCESSING" } }] } }]);
  await rejectsWith(client().waitValid("A1", "1.2", 5, 2, 30), 8, /not VALID within 2 minutes/);
  assert.equal(clock.sleeps.length, 4);
});

test("429 then 200 succeeds after sleeping 1 s", async () => {
  mock.on("GET", "/v1/apps", [{ status: 429, body: {} }, { status: 200, body: { data: [{ id: "A1" }] } }]);
  assert.equal(await client().appId("com.example.a"), "A1");
  assert.deepEqual(clock.sleeps, [1]);
});

test("persistent 503 is retried 1,2,4,8,16 s then fails with 6", async () => {
  mock.on("GET", "/v1/apps", [{ status: 503, body: {} }]);
  await rejectsWith(client().appId("com.example.a"), 6, /failed after 5 retries/);
  assert.deepEqual(clock.sleeps, [1, 2, 4, 8, 16]);
});

test("network errors are retried too", async () => {
  let calls = 0;
  const flaky = async (url, init) => {
    if (++calls < 3) throw new TypeError("fetch failed");
    return fetch(url, init);
  };
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [{ id: "A1" }] } }]);
  assert.equal(await client({ fetch: flaky }).appId("com.example.a"), "A1");
  assert.deepEqual(clock.sleeps, [1, 2]);
});

test("401 and 403 -> 6 with the key advice, no retry", async () => {
  for (const status of [401, 403]) {
    mock.on("GET", "/v1/apps", [{ status, body: {} }]);
    clock.sleeps.length = 0;
    await rejectsWith(client().appId("com.example.a"), 6, new RegExp(AUTH_MESSAGE));
    assert.deepEqual(clock.sleeps, []);
  }
});

test("setWhatsNew PATCHes an existing localization for the locale", async () => {
  mock.on("GET", "/v1/builds/B7/betaBuildLocalizations", [{ status: 200, body: { data: [{ id: "L1", attributes: { locale: "de-DE" } }, { id: "L2", attributes: { locale: "en-US" } }] } }]);
  mock.on("PATCH", "/v1/betaBuildLocalizations/L2", [{ status: 200, body: {} }]);
  await client().setWhatsNew("B7", "en-US", "hello");
  const patch = mock.requests.find((r) => r.method === "PATCH");
  assert.deepEqual(patch.body, { data: { type: "betaBuildLocalizations", id: "L2", attributes: { whatsNew: "hello" } } });
  assert.ok(!mock.requests.some((r) => r.method === "POST"));
});

test("setWhatsNew POSTs a new localization when none exists for the locale", async () => {
  mock.on("GET", "/v1/builds/B7/betaBuildLocalizations", [{ status: 200, body: { data: [] } }]);
  mock.on("POST", "/v1/betaBuildLocalizations", [{ status: 201, body: {} }]);
  await client().setWhatsNew("B7", "en-US", "hello");
  const post = mock.requests.find((r) => r.method === "POST");
  assert.deepEqual(post.body, { data: { type: "betaBuildLocalizations", attributes: { locale: "en-US", whatsNew: "hello" }, relationships: { build: { data: { type: "builds", id: "B7" } } } } });
});

test("setWhatsNew and addToGroup failures -> 9", async () => {
  mock.on("GET", "/v1/builds/B7/betaBuildLocalizations", [{ status: 404, body: {} }]);
  await rejectsWith(client().setWhatsNew("B7", "en-US", "x"), 9);
  mock.on("POST", "/v1/betaGroups/G1/relationships/builds", [{ status: 409, body: {} }]);
  await rejectsWith(client().addToGroup("B7", "G1"), 9);
});

test("groupId filters by app and name; missing group -> 6", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [{ id: "G1", attributes: { name: "Internal Testers" } }] } }]);
  assert.equal(await client().groupId("A1", "Internal Testers"), "G1");
  assert.equal(mock.requests[0].query["filter[app]"], "A1");
  assert.equal(mock.requests[0].query["filter[name]"], "Internal Testers");
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [] } }]);
  await rejectsWith(client().groupId("A1", "Nope"), 6);
});

test("addToGroup posts the build relationship body", async () => {
  mock.on("POST", "/v1/betaGroups/G1/relationships/builds", [{ status: 204, body: null }]);
  await client().addToGroup("B7", "G1");
  assert.deepEqual(mock.requests.find((r) => r.method === "POST").body, { data: [{ type: "builds", id: "B7" }] });
});

test("base URL: default, loopback and localhost allowed; anything else -> 2", () => {
  assert.equal(resolveBaseUrl(undefined), "https://api.appstoreconnect.apple.com");
  assert.equal(resolveBaseUrl("https://api.appstoreconnect.apple.com"), "https://api.appstoreconnect.apple.com");
  assert.equal(resolveBaseUrl("http://127.0.0.1:8080"), "http://127.0.0.1:8080");
  assert.equal(resolveBaseUrl("http://localhost:1"), "http://localhost:1");
  for (const bad of ["https://evil.example", "http://127.0.0.1", "http://127.0.0.1:80/x", "http://127.0.0.1.evil.example:80", "https://127.0.0.1:80"]) {
    assert.throws(() => resolveBaseUrl(bad), (e) => e.code === 2, bad);
  }
});

// ---- CLI, end to end ----
const home = fs.mkdtempSync(path.join(os.tmpdir(), "asc-home-"));
const baseEnv = () => ({ PATH: process.env.PATH, HOME: home, ASC_KEY_ID: key.keyId, ASC_ISSUER_ID: key.issuerId, ASC_KEY_PATH: key.keyPath, HIVEMILL_ASC_API_BASE: mock.baseUrl });
const cli = (args, env = baseEnv()) =>
  new Promise((resolve) => execFile(process.execPath, [ASC, ...args], { env, encoding: "utf8" }, (err, stdout, stderr) => resolve({ code: err ? err.code : 0, stdout, stderr })));

function assertNoSecrets(stderr) {
  for (const secret of [key.keyId, key.issuerId, key.keyPath, "PRIVATE KEY", path.basename(key.keyPath)]) assert.ok(!stderr.includes(secret), `stderr leaks ${secret}`);
  assert.ok(!/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/.test(stderr), "stderr leaks a JWT");
}

test("CLI: app-id against the mock prints only the value", async () => {
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [{ id: "A1" }] } }]);
  const r = await cli(["app-id", "--bundle-id", "com.example.a"]);
  assert.deepEqual([r.code, r.stdout, r.stderr], [0, "A1\n", ""]);
});

test("CLI: next-build, group-id, wait-valid, set-whats-new, add-to-group", async () => {
  mock.on("GET", "/v1/builds", [{ status: 200, body: (u) => ({ data: u.searchParams.has("filter[version]") ? [{ id: "B7", attributes: { processingState: "VALID" } }] : [build("b1", "4")] }) }]);
  assert.equal((await cli(["next-build", "--app-id", "A1", "--version", "1.2"])).stdout, "5\n");
  assert.equal((await cli(["wait-valid", "--app-id", "A1", "--version", "1.2", "--build", "4", "--timeout-min", "1", "--interval-s", "0"])).stdout, "B7\n");
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [{ id: "G1", attributes: { name: "Internal Testers" } }] } }]);
  assert.equal((await cli(["group-id", "--app-id", "A1", "--name", "Internal Testers"])).stdout, "G1\n");
  const notes = path.join(home, "notes.txt");
  fs.writeFileSync(notes, "hello\n");
  mock.on("GET", "/v1/builds/B7/betaBuildLocalizations", [{ status: 200, body: { data: [] } }]);
  mock.on("POST", "/v1/betaBuildLocalizations", [{ status: 201, body: {} }]);
  const w = await cli(["set-whats-new", "--build-id", "B7", "--locale", "en-US", "--notes-file", notes]);
  assert.deepEqual([w.code, w.stdout], [0, ""]);
  assert.equal(mock.requests.find((r) => r.method === "POST").body.data.attributes.whatsNew, "hello\n");
  mock.on("POST", "/v1/betaGroups/G1/relationships/builds", [{ status: 204, body: null }]);
  const g = await cli(["add-to-group", "--build-id", "B7", "--group-id", "G1"]);
  assert.deepEqual([g.code, g.stdout], [0, "added\n"]);
});

test("CLI failures: exit codes and no secrets on stderr", async () => {
  const cases = [];
  mock.on("GET", "/v1/apps", [{ status: 401, body: {} }]);
  cases.push([await cli(["app-id", "--bundle-id", "com.example.a"]), 6, /App Manager/]);
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [] } }]);
  cases.push([await cli(["app-id", "--bundle-id", "com.example.a"]), 6, /no App Store Connect app/]);
  cases.push([await cli(["bogus"]), 2, /unknown/]);
  cases.push([await cli(["constructor"]), 2, /unknown/]);
  cases.push([await cli(["app-id", "--bundle-id", "x", "--extra", "y"]), 2, /unknown flag/]);
  cases.push([await cli(["app-id"]), 2, /needs/]);
  cases.push([await cli(["app-id", "--bundle-id", "x"], { ...baseEnv(), ASC_KEY_ID: "" }), 4, /ASC_KEY_ID/]);
  cases.push([await cli(["app-id", "--bundle-id", "x"], { ...baseEnv(), ASC_KEY_PATH: path.join(home, "missing.p8") }), 4, /cannot read/]);
  const rsa = path.join(home, "rsa.p8");
  fs.writeFileSync(rsa, crypto.generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey.export({ type: "pkcs8", format: "pem" }), { mode: 0o600 });
  cases.push([await cli(["app-id", "--bundle-id", "x"], { ...baseEnv(), ASC_KEY_PATH: rsa }), 4, /P-256/]);
  cases.push([await cli(["app-id", "--bundle-id", "x"], { ...baseEnv(), HIVEMILL_ASC_API_BASE: "https://evil.example" }), 2, /not allowed/]);
  const notes = path.join(home, "n.txt");
  fs.writeFileSync(notes, "x");
  mock.on("GET", "/v1/builds/B7/betaBuildLocalizations", [{ status: 404, body: {} }]);
  clock.sleeps.length = 0;
  cases.push([await cli(["set-whats-new", "--build-id", "B7", "--locale", "en-US", "--notes-file", notes]), 9, /What to Test/]);
  for (const [r, code, re] of cases) {
    assert.equal(r.code, code, r.stderr);
    assert.match(r.stderr, re);
    assert.equal(r.stdout, "");
    assertNoSecrets(r.stderr);
  }
});

test("CLI: https://evil.example override with a missing key exits 2 before touching credentials", async () => {
  const r = await cli(["app-id", "--bundle-id", "com.example.a"], { PATH: process.env.PATH, HOME: home, HIVEMILL_ASC_API_BASE: "https://evil.example", ASC_KEY_ID: "x", ASC_ISSUER_ID: "y", ASC_KEY_PATH: "/nonexistent" });
  assert.equal(r.code, 2);
});

// ---- T-1425: group rules, exact name, timeout ----
const grp = (id, name, internal = true, all = false) => ({ id, type: "betaGroups", attributes: { name, isInternalGroup: internal, hasAccessToAllBuilds: all } });
const posts = () => mock.requests.filter((r) => r.method === "POST");
const POST_PATH = "/v1/betaGroups/G1/relationships/builds";

test("group lookup: 'Internal Testers 2' listed first, the exact 'Internal Testers' is picked", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [grp("G2", "Internal Testers 2"), grp("G1", "Internal Testers")] } }]);
  assert.equal(await client().groupId("A1", "Internal Testers"), "G1");
  assert.equal(mock.requests[0].query["filter[name]"], "Internal Testers");
});

test("group lookup: a near match only -> 6 beta group not found; case differs -> 6", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [grp("G2", "Internal Testers 2"), grp("G3", "internal testers")] } }]);
  await rejectsWith(client().groupInfo("A1", "Internal Testers"), 6, /beta group not found: Internal Testers/);
});

test("group lookup: two exact matches -> 6 ambiguous, also across pages", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [grp("G1", "Internal Testers"), grp("G9", "Internal Testers")] } }]);
  await rejectsWith(client().groupInfo("A1", "Internal Testers"), 6, /beta group name is ambiguous: Internal Testers/);
  mock.on("GET", "/v1/betaGroups", [
    { status: 200, body: { data: [grp("G1", "Internal Testers")], links: { next: `${mock.baseUrl}/v1/betaGroups?cursor=2` } } },
    { status: 200, body: { data: [grp("G9", "Internal Testers")] } },
  ]);
  await rejectsWith(client().groupInfo("A1", "Internal Testers"), 6, /ambiguous/);
});

test("groupInfo returns id, isInternal and allBuilds", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [grp("G1", "Internal Testers", true, true)] } }]);
  assert.deepEqual(await client().groupInfo("A1", "Internal Testers"), { id: "G1", name: "Internal Testers", isInternal: true, allBuilds: true });
});

test("CLI group-info prints one tab-separated line for internal/all, internal/selected and external", async () => {
  const cases = [[grp("G1", "Internal Testers", true, true), "G1\tinternal\tall\tInternal Testers\n"], [grp("G1", "Internal Testers", true, false), "G1\tinternal\tselected\tInternal Testers\n"], [grp("G1", "Internal Testers", false, false), "G1\texternal\tselected\tInternal Testers\n"]];
  for (const [g, want] of cases) {
    mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [g] } }]);
    const r = await cli(["group-info", "--app-id", "A1", "--name", "Internal Testers"]);
    assert.deepEqual([r.code, r.stdout], [0, want]);
    assertNoSecrets(r.stderr);
  }
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [] } }]);
  const miss = await cli(["group-info", "--app-id", "A1", "--name", "Internal Testers"]);
  assert.deepEqual([miss.code, miss.stdout], [6, ""]);
  assert.match(miss.stderr, /beta group not found: Internal Testers/);
});

test("addToGroup: internal with all builds -> skipped-all-builds, zero POSTs", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", true, true) }]);
  assert.equal(await client().addToGroup("B7", "G1"), "skipped-all-builds");
  assert.equal(posts().length, 0);
});

test("addToGroup: internal with selected builds -> added, one POST", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", true, false) }]);
  mock.on("POST", POST_PATH, [{ status: 204, body: null }]);
  assert.equal(await client().addToGroup("B7", "G1"), "added");
  assert.equal(posts().length, 1);
});

test("addToGroup: external without submitExternal -> skipped-external, zero POSTs", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", false, false) }]);
  assert.equal(await client().addToGroup("B7", "G1"), "skipped-external");
  assert.equal(await client().addToGroup("B7", "G1", { submitExternal: false }), "skipped-external");
  assert.equal(posts().length, 0);
});

test("addToGroup: external with submitExternal -> added, one POST", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", false, false) }]);
  mock.on("POST", POST_PATH, [{ status: 204, body: null }]);
  assert.equal(await client().addToGroup("B7", "G1", { submitExternal: true }), "added");
  assert.equal(posts().length, 1);
});

test("addToGroup: a 422 on an internal group with selected builds -> 9, not skipped", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", true, false) }]);
  mock.on("POST", POST_PATH, [{ status: 422, body: {} }]);
  await rejectsWith(client().addToGroup("B7", "G1"), 9, /422/);
});

test("CLI add-to-group: status line on stdout; --submit-external opts an external group in", async () => {
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", true, true) }]);
  let r = await cli(["add-to-group", "--build-id", "B7", "--group-id", "G1"]);
  assert.deepEqual([r.code, r.stdout], [0, "skipped-all-builds\n"]);
  assert.match(r.stderr, /group G1: skipped: gets all builds/);
  assertNoSecrets(r.stderr);
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", false, false) }]);
  r = await cli(["add-to-group", "--build-id", "B7", "--group-id", "G1"]);
  assert.deepEqual([r.code, r.stdout], [0, "skipped-external\n"]);
  assert.match(r.stderr, /set submitExternal to add it/);
  assert.equal(posts().length, 0);
  mock.on("POST", POST_PATH, [{ status: 204, body: null }]);
  r = await cli(["add-to-group", "--build-id", "B7", "--group-id", "G1", "--submit-external"]);
  assert.deepEqual([r.code, r.stdout], [0, "added\n"]);
  assert.equal(posts().length, 1);
  mock.on("GET", "/v1/betaGroups/G1", [{ status: 200, body: groupBody("G1", true, false) }]);
  mock.on("POST", POST_PATH, [{ status: 422, body: {} }]);
  r = await cli(["add-to-group", "--build-id", "B7", "--group-id", "G1"]);
  assert.equal(r.code, 9);
  assert.equal(r.stdout, "");
  assertNoSecrets(r.stderr);
});

test("timeout: one hanging response is retried (sleeps [1]) then succeeds", async () => {
  mock.on("GET", "/v1/apps", [{ status: 200, hang: true }, { status: 200, body: { data: [{ id: "A1" }] } }]);
  assert.equal(await client({ timeoutMs: 50 }).appId("com.example.a"), "A1");
  assert.deepEqual(clock.sleeps, [1]);
});

test("timeout: always hanging -> 6 after 5 retries", async () => {
  mock.on("GET", "/v1/apps", [{ status: 200, hang: true }]);
  await rejectsWith(client({ timeoutMs: 50 }).appId("com.example.a"), 6, /failed after 5 retries \(network error\)/);
  assert.deepEqual(clock.sleeps, [1, 2, 4, 8, 16]);
});

// ---- two beta groups with the same name: one internal, one external, plus an unrelated one ----
const twins = () => [grp("g-int-1", "Beta Testers", true, true), grp("g-ext-1", "Beta Testers", false, false), grp("g-other", "Other Group", true, false)];
const serveTwins = () =>
  mock.on("GET", "/v1/betaGroups", [{
    status: 200,
    body: (u) => ({ data: twins().filter((g) => (u.searchParams.has("filter[id]") ? g.id === u.searchParams.get("filter[id]") : g.attributes.name === u.searchParams.get("filter[name]"))) }),
  }]);

test("twin groups: name only -> 6 ambiguous with both ids and both isInternalGroup values, in API order", async () => {
  serveTwins();
  await rejectsWith(client().groupInfo("A1", { name: "Beta Testers" }), 6, /beta group name is ambiguous: Beta Testers: g-int-1 \(isInternalGroup true\), g-ext-1 \(isInternalGroup false\); set testerGroup to an object with id, or with name and internal/);
});

test("twin groups: name + internal picks the matching group", async () => {
  serveTwins();
  assert.equal((await client().groupInfo("A1", { name: "Beta Testers", internal: true })).id, "g-int-1");
  assert.equal((await client().groupInfo("A1", { name: "Beta Testers", internal: false })).id, "g-ext-1");
});

test("twin groups: id picks that group and sends no filter[name]", async () => {
  serveTwins();
  const g = await client().groupInfo("A1", { id: "g-ext-1" });
  assert.deepEqual(g, { id: "g-ext-1", name: "Beta Testers", isInternal: false, allBuilds: false });
  const gets = mock.requests.filter((r) => r.path === "/v1/betaGroups");
  assert.equal(gets.length, 1);
  assert.equal(gets[0].query["filter[id]"], "g-ext-1");
  assert.equal(gets[0].query["filter[app]"], "A1");
  assert.ok(!("filter[name]" in gets[0].query));
});

test("twin groups: an id the API echoes with other ids is matched exactly; an unknown id -> 6 not found", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: twins() } }]);
  assert.equal((await client().groupInfo("A1", { id: "g-other" })).id, "g-other");
  await rejectsWith(client().groupInfo("A1", { id: "g-missing" }), 6, /beta group not found: id g-missing/);
});

test("twin groups: name + internal with no match -> 6 not found naming the internal value", async () => {
  mock.on("GET", "/v1/betaGroups", [{ status: 200, body: { data: [grp("g-int-1", "Beta Testers", true, true)] } }]);
  await rejectsWith(client().groupInfo("A1", { name: "Beta Testers", internal: false }), 6, /beta group not found: Beta Testers \(internal false\)/);
});

test("CLI group-info on the twins: 4 tab-separated fields, ambiguity exit 6, usage errors exit 2", async () => {
  serveTwins();
  const r = await cli(["group-info", "--app-id", "A1", "--name", "Beta Testers", "--internal", "false"]);
  assert.deepEqual([r.code, r.stdout], [0, "g-ext-1\texternal\tselected\tBeta Testers\n"]);
  assert.equal(r.stdout.trimEnd().split("\t").length, 4);
  const byId = await cli(["group-info", "--app-id", "A1", "--id", "g-int-1"]);
  assert.deepEqual([byId.code, byId.stdout], [0, "g-int-1\tinternal\tall\tBeta Testers\n"]);
  const amb = await cli(["group-info", "--app-id", "A1", "--name", "Beta Testers"]);
  assert.equal(amb.code, 6);
  assert.match(amb.stderr, /g-int-1 \(isInternalGroup true\), g-ext-1 \(isInternalGroup false\)/);
  const usage = [
    ["--id", "g-1", "--name", "Beta Testers"],
    [],
    ["--id", "g-1", "--internal", "true"],
    ["--name", "Beta Testers", "--internal", "maybe"],
  ];
  for (const extra of usage) assert.equal((await cli(["group-info", "--app-id", "A1", ...extra])).code, 2, extra.join(" "));
  const help = await cli(["--help"]);
  assert.equal(help.code, 0);
  assert.match(help.stdout, /group-info --app-id A \(--id ID \| --name N \[--internal true\|false\]\)/);
});
