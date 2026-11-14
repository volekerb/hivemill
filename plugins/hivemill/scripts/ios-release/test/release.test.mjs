// Shared harness for release.test.mjs: a temp git project, a temp HOME, PATH stubs and the ASC mock.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync, spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { makeFakeKey } from "./helpers/fakekey.mjs";
import { startMock } from "./helpers/mock-asc.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const RELEASE = path.join(HERE, "..", "release.sh");
export const STUBS = path.join(HERE, "stubs");
export const JWT_RE = /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/;

const GIT_ENV = { PATH: "/usr/bin:/bin", GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@example.com", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@example.com" };
export const git = (cwd, ...args) => execFileSync("git", args, { cwd, env: { ...GIT_ENV, HOME: cwd }, encoding: "utf8" }).trim();

export const baseConfig = () => ({
  project: "ios/MyApp.xcodeproj",
  scheme: "MyApp",
  bundleId: "com.example.myapp",
  teamId: "ABCDE12345",
  marketingVersion: { file: "ios/Config/Version.xcconfig", regex: "^MARKETING_VERSION = (.+)$" },
  testerGroup: ["Internal Testers"],
  notes: { path: "docs/notes-{build}.md" },
  pollIntervalSeconds: 0,
});

// Tester groups the mock knows, by name.
export const defaultGroups = () => ({ "Internal Testers": { id: "G1", isInt: true, all: false } });

// setup(t, { config, groups, files, state, creds }) -> ctx. Everything is removed after the test.
export async function setup(t, opts = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ios-release-test-"));
  const mock = await startMock();
  const key = makeFakeKey();
  t.after(async () => {
    await mock.close();
    fs.rmSync(root, { recursive: true, force: true });
    fs.rmSync(key.dir, { recursive: true, force: true });
  });
  const home = path.join(root, "home");
  const repo = path.join(root, "repo");
  const logDir = path.join(root, "log");
  for (const d of [home, repo, logDir]) fs.mkdirSync(d, { recursive: true });
  const ctx = { root, home, repo, logDir, mock, key, stubLog: path.join(logDir, "xcodebuild.log"), marker: path.join(root, "marker") };

  const config = typeof opts.config === "function" ? opts.config(baseConfig(), ctx) : { ...baseConfig(), ...(opts.config ?? {}) };
  const files = {
    ".hivemill/ios-release.json": JSON.stringify(config, null, 2),
    "ios/MyApp.xcodeproj/project.pbxproj": "// project\n",
    "ios/Config/Version.xcconfig": "MARKETING_VERSION = 1.2.3\n",
    "docs/notes-5.md": "## What to Test\n- open the app\n",
    "sub/.keep": "",
    ...(opts.files ?? {}),
  };
  for (const [rel, text] of Object.entries(files)) {
    fs.mkdirSync(path.dirname(path.join(repo, rel)), { recursive: true });
    fs.writeFileSync(path.join(repo, rel), text);
  }
  git(repo, "init", "-q");
  git(repo, "symbolic-ref", "HEAD", "refs/heads/main");
  git(repo, "add", "-A");
  git(repo, "commit", "-q", "-m", "first");
  if (opts.secondCommit) {
    fs.writeFileSync(path.join(repo, "second.txt"), "2\n");
    git(repo, "add", "-A");
    git(repo, "commit", "-q", "-m", "second");
  }
  ctx.head = git(repo, "rev-parse", "HEAD");

  routes(mock, opts.groups ?? defaultGroups(), opts.state ?? "VALID");
  ctx.creds = opts.creds ?? "env";
  if (ctx.creds === "profile") writeProfile(ctx, 0o600);
  return ctx;
}

export function writeProfile(ctx, mode, name = "default") {
  const dir = path.join(ctx.home, ".config", "hivemill", "asc");
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, `${name}.env`);
  fs.writeFileSync(file, `ASC_KEY_ID=${ctx.key.keyId}\nASC_ISSUER_ID=${ctx.key.issuerId}\nASC_KEY_PATH=${ctx.key.keyPath}\n`);
  fs.chmodSync(file, mode);
  return file;
}

function routes(mock, groups, state) {
  mock.on("GET", "/v1/apps", [{ status: 200, body: { data: [{ id: "APP1" }] } }]);
  mock.on("GET", "/v1/builds", [{
    status: 200,
    body: (url) => (url.searchParams.has("filter[version]")
      ? { data: [{ id: "BLD1", attributes: { version: "5", processingState: state } }] }
      : { data: [{ id: "B4", attributes: { version: "4" } }] }),
  }]);
  const asList = (g) => (Array.isArray(g) ? g : [g]);
  const all = Object.entries(groups).flatMap(([name, g]) => asList(g).map((x) => ({ name, ...x })));
  mock.on("GET", "/v1/betaGroups", [{
    status: 200,
    body: (url) => ({
      data: all
        .filter((g) => (url.searchParams.has("filter[id]") ? g.id === url.searchParams.get("filter[id]") : g.name === url.searchParams.get("filter[name]")))
        .map((g) => ({ id: g.id, attributes: { name: g.name, isInternalGroup: g.isInt, hasAccessToAllBuilds: g.all } })),
    }),
  }]);
  for (const g of all) {
    mock.on("GET", `/v1/betaGroups/${g.id}`, [{ status: 200, body: { data: { id: g.id, type: "betaGroups", attributes: { isInternalGroup: g.isInt, hasAccessToAllBuilds: g.all } } } }]);
    mock.on("POST", `/v1/betaGroups/${g.id}/relationships/builds`, [{ status: 204 }]);
  }
  mock.on("GET", "/v1/builds/BLD1/betaBuildLocalizations", [{ status: 200, body: { data: [] } }]);
  mock.on("POST", "/v1/betaBuildLocalizations", [{ status: 201, body: { data: { id: "L1" } } }]);
}

// run(ctx, args, env) -> { code, stdout, stderr, out } (out = stdout + stderr)
export function run(ctx, args = [], env = {}) {
  const e = {
    PATH: `${STUBS}:${path.dirname(process.execPath)}:/usr/bin:/bin`,
    HOME: ctx.home,
    STUB_LOG: ctx.stubLog,
    HIVEMILL_ASC_API_BASE: ctx.mock.baseUrl,
    ...(ctx.creds === "env" ? { ASC_KEY_ID: ctx.key.keyId, ASC_ISSUER_ID: ctx.key.issuerId, ASC_KEY_PATH: ctx.key.keyPath } : {}),
    ...env,
  };
  return new Promise((resolve, reject) => {
    const child = spawn("/bin/bash", [RELEASE, ...args], { cwd: ctx.repo, env: e });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), 120000);
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("error", reject);
    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr, out: stdout + stderr });
    });
  });
}

export const stubRecords = (ctx) => {
  if (!fs.existsSync(ctx.stubLog)) return [];
  return fs.readFileSync(ctx.stubLog, "utf8").split("--- xcodebuild\n").filter(Boolean).map((r) => ({
    argv: /^ARGV: (.*)$/m.exec(r)[1],
    path: /^PATH: (.*)$/m.exec(r)[1],
    envNames: /^ENVNAMES: (.*)$/m.exec(r)[1].trim(),
  }));
};
export const byKind = (ctx, word) => stubRecords(ctx).filter((r) => r.argv.split(" ").includes(word));

export const summaryOf = (r) => JSON.parse(r.stdout.trimEnd().split("\n").pop());
export const writeConfig = (ctx, cfg) => fs.writeFileSync(path.join(ctx.repo, ".hivemill", "ios-release.json"), JSON.stringify(cfg, null, 2));
export const commitAll = (ctx) => {
  git(ctx.repo, "add", "-A");
  git(ctx.repo, "commit", "-q", "-m", "change");
};

// ---------------------------------------------------------------------------------------------------------------
// Tests. Each case builds its own temp project, HOME, stubs log and mock, so the cases are independent.
// ---------------------------------------------------------------------------------------------------------------
import { describe, it } from "node:test";
import assert from "node:assert/strict";

const AUTH = ["-authenticationKeyPath", "-authenticationKeyID", "-authenticationKeyIssuerID"];
const RESET = "/usr/bin:/bin:/usr/sbin:/sbin";
const postsTo = (ctx, id) => ctx.mock.requests.filter((r) => r.method === "POST" && r.path === `/v1/betaGroups/${id}/relationships/builds`);
const nonGet = (ctx) => ctx.mock.requests.filter((r) => r.method !== "GET");
const fourGroups = () => ({
  "Group A": { id: "GA", isInt: true, all: true },
  "Group B": { id: "GB", isInt: true, all: false },
  "Group C": { id: "GC", isInt: false, all: false },
  "Group D": { id: "GD", isInt: true, all: false },
});
// Two groups with the SAME placeholder name (one internal, one external) plus an unrelated one.
const twinGroups = () => ({
  "Beta Testers": [{ id: "g-int-1", isInt: true, all: true }, { id: "g-ext-1", isInt: false, all: false }],
  "Other Group": { id: "g-other", isInt: true, all: false },
});
const withTesters = (testerGroup) => (c) => ({ ...c, testerGroup });
const fourConfig = (extra = {}) => (c) => ({ ...c, testerGroup: ["Group A", "Group B", "Group C", "Group D"], ...extra });

describe("release.sh", { concurrency: 4 }, () => {
  it("bash -n release.sh passes and the file avoids bash 4 features", () => {
    execFileSync("bash", ["-n", RELEASE]);
    const text = fs.readFileSync(RELEASE, "utf8");
    assert.ok(text.startsWith("#!/usr/bin/env bash\n"));
    assert.doesNotMatch(text, /declare -A|mapfile|readarray|[|]&|&>>|set -x/);
    assert.doesNotMatch(text, /[a-zA-Z_]+,,|[a-zA-Z_]+\^\^/);
    assert.doesNotMatch(text, /^\s*(source|[.]) /m);
    assert.doesNotMatch(text, /eval/);
    assert.ok(fs.statSync(RELEASE).mode & 0o100, "release.sh is executable");
  });

  it("default signing: real run exits 0, no -authenticationKey flag, CURRENT_PROJECT_VERSION is the next build, ExportOptions keys", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    const recs = stubRecords(ctx);
    assert.ok(recs.length >= 3);
    for (const rec of recs) assert.ok(!rec.argv.includes("-authenticationKey"), rec.argv);
    const archive = byKind(ctx, "archive")[0].argv;
    assert.match(archive, /CURRENT_PROJECT_VERSION=5( |$)/);
    assert.match(archive, /-allowProvisioningUpdates/);
    const exp = byKind(ctx, "-exportArchive")[0].argv;
    assert.match(exp, /-allowProvisioningUpdates/);
    const plist = fs.readFileSync(path.join(ctx.logDir, "ExportOptions.plist"), "utf8");
    assert.match(plist, /<key>method<\/key><string>app-store-connect<\/string>/);
    assert.match(plist, /<key>destination<\/key><string>upload<\/string>/);
    assert.match(plist, /<key>signingStyle<\/key><string>automatic<\/string>/);
    assert.match(plist, /<key>manageAppVersionAndBuildNumber<\/key><false\/>/);
    assert.match(plist, /<key>teamID<\/key><string>ABCDE12345<\/string>/);
  });

  for (const how of ["flag", "config"]) {
    it(`apiKey signing by ${how}: archive and export carry the three -authenticationKey flags with the values`, async (t) => {
      const ctx = await setup(t, { config: how === "config" ? { signing: "apiKey" } : {} });
      const r = await run(ctx, how === "flag" ? ["--signing", "apiKey"] : []);
      assert.equal(r.code, 0, r.out);
      for (const call of [byKind(ctx, "archive")[0].argv, byKind(ctx, "-exportArchive")[0].argv]) {
        for (const f of AUTH) assert.ok(call.includes(f), `${f} missing in ${call}`);
        assert.ok(call.includes(`-authenticationKeyPath ${ctx.key.keyPath}`));
        assert.ok(call.includes(`-authenticationKeyID ${ctx.key.keyId}`));
        assert.ok(call.includes(`-authenticationKeyIssuerID ${ctx.key.issuerId}`));
      }
      assert.equal(summaryOf(r).signing, "apiKey");
    });
  }

  it("PATH reset: every xcodebuild call runs with exactly /usr/bin:/bin:/usr/sbin:/sbin", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    const recs = stubRecords(ctx);
    assert.ok(recs.length >= 3);
    for (const rec of recs) assert.equal(rec.path, RESET);
  });

  for (const signing of ["xcodeAccount", "apiKey"]) {
    it(`env hygiene (${signing}): no xcodebuild call sees an ASC_* or HIVEMILL_REDACT* variable`, async (t) => {
      const ctx = await setup(t);
      const r = await run(ctx, ["--signing", signing]);
      assert.equal(r.code, 0, r.out);
      for (const rec of stubRecords(ctx)) assert.equal(rec.envNames, "", rec.argv);
    });
  }

  it("order and group rules: VALID poll, then whatsNew, then POSTs for B and D only; submitExternal false", async (t) => {
    const ctx = await setup(t, { config: fourConfig(), groups: fourGroups() });
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    const reqs = ctx.mock.requests;
    const lastPoll = reqs.map((x, i) => (x.method === "GET" && x.path === "/v1/builds" && "filter[version]" in x.query ? i : -1)).filter((i) => i >= 0).pop();
    const whatsNew = reqs.findIndex((x) => x.method === "POST" && x.path === "/v1/betaBuildLocalizations");
    const posts = reqs.map((x, i) => [x, i]).filter(([x]) => x.method === "POST" && x.path.endsWith("/relationships/builds"));
    assert.ok(lastPoll >= 0 && whatsNew > lastPoll, "whatsNew after the VALID poll");
    assert.deepEqual(posts.map(([x]) => x.path), ["/v1/betaGroups/GB/relationships/builds", "/v1/betaGroups/GD/relationships/builds"]);
    assert.ok(posts[0][1] > whatsNew, "first group POST after whatsNew");
    assert.equal(postsTo(ctx, "GA").length, 0);
    assert.equal(postsTo(ctx, "GC").length, 0);
    assert.deepEqual(summaryOf(r).groups, [
      { name: "Group A", status: "skipped-all-builds" },
      { name: "Group B", status: "added" },
      { name: "Group C", status: "skipped-external" },
      { name: "Group D", status: "added" },
    ]);
  });

  it("submitExternal true: the external group is POSTed too", async (t) => {
    const ctx = await setup(t, { config: fourConfig({ submitExternal: true }), groups: fourGroups() });
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    assert.equal(postsTo(ctx, "GC").length, 1);
    assert.equal(postsTo(ctx, "GA").length, 0);
    assert.equal(summaryOf(r).groups[2].status, "added");
  });

  describe("tester group selectors on two groups with the same name", () => {
    it("dry run with the plain name: exit 6, summary ok false, the error lists both ids", async (t) => {
      const ctx = await setup(t, { config: withTesters("Beta Testers"), groups: twinGroups() });
      const r = await run(ctx, ["--dry-run"]);
      assert.equal(r.code, 6, r.out);
      assert.match(r.stderr, /ambiguous/);
      assert.match(r.stderr, /g-int-1 \(isInternalGroup true\)/);
      assert.match(r.stderr, /g-ext-1 \(isInternalGroup false\)/);
      const s = summaryOf(r);
      assert.equal(s.ok, false);
      assert.equal(s.exitCode, 6);
      assert.match(s.error, /g-int-1/);
      assert.match(s.error, /g-ext-1/);
    });

    it("dry run with name + internal true: exit 0 and that group's decision line", async (t) => {
      const ctx = await setup(t, { config: withTesters([{ name: "Beta Testers", internal: true }]), groups: twinGroups() });
      const r = await run(ctx, ["--dry-run"]);
      assert.equal(r.code, 0, r.out);
      assert.match(r.out, /Beta Testers: would skip \(gets all builds\)/);
      assert.deepEqual(summaryOf(r).groups, [{ name: "Beta Testers", status: "would-skip" }]);
    });

    it("dry run with the id of the external group and submitExternal false: would skip (external)", async (t) => {
      const ctx = await setup(t, { config: withTesters([{ id: "g-ext-1" }]), groups: twinGroups() });
      const r = await run(ctx, ["--dry-run"]);
      assert.equal(r.code, 0, r.out);
      assert.match(r.out, /Beta Testers: would skip \(external; submitExternal false\)/);
      assert.ok(!ctx.mock.requests.some((q) => q.query["filter[name]"] !== undefined), "an id entry must not send filter[name]");
    });

    it("real run with name + internal true on an internal all-builds group: skipped-all-builds, zero POSTs to it", async (t) => {
      const ctx = await setup(t, { config: withTesters([{ name: "Beta Testers", internal: true }]), groups: twinGroups() });
      const r = await run(ctx);
      assert.equal(r.code, 0, r.out);
      assert.deepEqual(summaryOf(r).groups, [{ name: "Beta Testers", status: "skipped-all-builds" }]);
      assert.equal(postsTo(ctx, "g-int-1").length, 0);
      assert.equal(postsTo(ctx, "g-ext-1").length, 0);
    });

    it("two entries that resolve to one group exit 6 naming both entry indexes and the id", async (t) => {
      const ctx = await setup(t, { config: withTesters([{ id: "g-ext-1" }, { name: "Beta Testers", internal: false }]), groups: twinGroups() });
      const r = await run(ctx, ["--dry-run"]);
      assert.equal(r.code, 6, r.out);
      assert.match(r.stderr, /testerGroup entries 0 and 1 are the same group g-ext-1/);
      assert.equal(summaryOf(r).exitCode, 6);
    });
  });

  it("unknown group name exits 6 before any build", async (t) => {
    const ctx = await setup(t, { config: { testerGroup: ["No Such Group"] } });
    const r = await run(ctx);
    assert.equal(r.code, 6, r.out);
    assert.match(r.stderr, /beta group not found: No Such Group/);
    assert.equal(byKind(ctx, "archive").length, 0);
    assert.equal(summaryOf(r).exitCode, 6);
  });

  it("dry run: exit 0, next build, one decision line per group, no writes, no archive, no gate", async (t) => {
    const ctx = await setup(t, {
      config: (c, x) => ({ ...c, testerGroup: ["Group A", "Group B", "Group C"], gates: [["/bin/sh", "-c", 'touch "$0"', x.marker]] }),
      groups: fourGroups(),
    });
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 0, r.out);
    assert.match(r.out, /next build: 5/);
    assert.match(r.out, /Group A: would skip \(gets all builds\)/);
    assert.match(r.out, /Group B: would add/);
    assert.match(r.out, /Group C: would skip \(external; submitExternal false\)/);
    assert.equal(nonGet(ctx).length, 0, JSON.stringify(nonGet(ctx)));
    assert.equal(byKind(ctx, "archive").length, 0);
    assert.equal(byKind(ctx, "-exportArchive").length, 0);
    assert.ok(!fs.existsSync(ctx.marker), "the gate must not run in a dry run");
    const s = summaryOf(r);
    assert.equal(s.mode, "dry-run");
    assert.deepEqual(s.groups.map((g) => g.status), ["would-skip", "would-add", "would-skip"]);
  });

  it("dry run command preview: apiKey values are [REDACTED]; default signing shows no -authenticationKey", async (t) => {
    const ctx = await setup(t);
    const api = await run(ctx, ["--dry-run", "--signing", "apiKey"]);
    assert.equal(api.code, 0, api.out);
    const archiveLine = api.stderr.split("\n").find((l) => l.startsWith("archive: "));
    const exportLine = api.stderr.split("\n").find((l) => l.startsWith("export: "));
    for (const line of [archiveLine, exportLine]) {
      assert.match(line, /-authenticationKeyPath \[REDACTED\] -authenticationKeyID \[REDACTED\] -authenticationKeyIssuerID \[REDACTED\]/);
    }
    const def = await run(ctx, ["--dry-run"]);
    assert.equal(def.code, 0, def.out);
    assert.ok(def.stderr.includes("archive: ") && !def.stderr.includes("-authenticationKey"));
  });

  for (const mode of ["real", "dry-run"]) {
    for (const creds of ["env", "profile"]) {
      for (const signing of ["xcodeAccount", "apiKey"]) {
        it(`SECRET TEST: ${mode} run, ${creds} credentials, ${signing}: no secret in stdout or stderr`, async (t) => {
          const ctx = await setup(t, { creds });
          const r = await run(ctx, [...(mode === "dry-run" ? ["--dry-run"] : []), "--signing", signing]);
          assert.equal(r.code, 0, r.out);
          for (const secret of [ctx.key.keyId, ctx.key.issuerId, ctx.key.keyPath, path.basename(ctx.key.keyPath), "PRIVATE KEY"]) {
            assert.ok(!r.out.includes(secret), `output leaks ${secret === ctx.key.keyId ? "the key id" : "a secret"}`);
          }
          assert.doesNotMatch(r.out, JWT_RE);
          assert.ok(ctx.mock.requests.some((x) => /^Bearer .+/.test(x.authorization ?? "")), "the mock saw a Bearer token");
          if (mode === "real" && signing === "apiKey") {
            const call = byKind(ctx, "archive")[0].argv;
            for (const v of [ctx.key.keyId, ctx.key.issuerId, ctx.key.keyPath]) assert.ok(call.includes(v), "positive control: the stub argv holds the value");
          }
          if (mode === "real" && signing === "xcodeAccount") assert.ok(!byKind(ctx, "archive")[0].argv.includes(ctx.key.keyId));
        });
      }
    }
  }

  it("credentials: an insecure profile file (0644) is refused with exit 4 and chmod 600", async (t) => {
    const ctx = await setup(t, { creds: "none" });
    writeProfile(ctx, 0o644);
    const r = await run(ctx, [], { HIVEMILL_ASC_KEYCHAIN: "on", STUB_SECURITY_LOG: path.join(ctx.logDir, "sec.log") });
    assert.equal(r.code, 4, r.out);
    assert.match(r.stderr, /chmod 600/);
    assert.ok(!fs.existsSync(path.join(ctx.logDir, "sec.log")), "the Keychain was not consulted");
  });

  it("credentials: no credentials at all exits 4", async (t) => {
    const ctx = await setup(t, { creds: "none" });
    const r = await run(ctx);
    assert.equal(r.code, 4, r.out);
    assert.equal(summaryOf(r).exitCode, 4);
  });

  it("credentials: Keychain with HIVEMILL_ASC_KEYCHAIN=on supplies the values through the security stub", async (t) => {
    const ctx = await setup(t, { creds: "none" });
    const log = path.join(ctx.logDir, "sec.log");
    const r = await run(ctx, ["--dry-run"], {
      HIVEMILL_ASC_KEYCHAIN: "on",
      STUB_SECURITY_LOG: log,
      STUB_SECURITY_ASC_KEY_ID: ctx.key.keyId,
      STUB_SECURITY_ASC_ISSUER_ID: ctx.key.issuerId,
      STUB_SECURITY_ASC_KEY_PATH: ctx.key.keyPath,
    });
    assert.equal(r.code, 0, r.out);
    assert.match(r.stderr, /source: keychain:default/);
    assert.match(fs.readFileSync(log, "utf8"), /find-generic-password -s hivemill-asc-default -a ASC_KEY_ID -w/);
  });

  it("failure: archive exits 65 -> release exit 7", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, [], { STUB_XCODEBUILD_EXIT: "65" });
    assert.equal(r.code, 7, r.out);
    assert.equal(byKind(ctx, "-exportArchive").length, 0);
  });

  it("failure: export prints a cloud signing error -> exit 7 and the signing hint", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, [], { STUB_XCODEBUILD_EXPORT_EXIT: "70", STUB_XCODEBUILD_EXPORT_STDERR: "error: Cloud signing permission error" });
    assert.equal(r.code, 7, r.out);
    assert.match(r.stderr, /signing failed: in xcodeAccount mode sign in to Xcode \(Settings > Accounts\) with an account in the team; apiKey mode needs an Admin key \(App Manager keys cannot cloud-sign\); see README/);
  });

  it("failure: processing ends INVALID -> exit 8", async (t) => {
    const ctx = await setup(t, { state: "INVALID" });
    const r = await run(ctx);
    assert.equal(r.code, 8, r.out);
    assert.equal(postsTo(ctx, "G1").length, 0);
  });

  it("failure: a group POST is rejected -> exit 9, then --resume-build 5 succeeds with no xcodebuild and no gates", async (t) => {
    const ctx = await setup(t, {
      config: (c, x) => ({ ...c, testerGroup: ["Group A", "Group B"], gates: [["/bin/sh", "-c", 'echo ran >> "$0"', x.marker]] }),
      groups: fourGroups(),
    });
    ctx.mock.on("POST", "/v1/betaGroups/GB/relationships/builds", [{ status: 422, body: { errors: [{ title: "no" }] } }, { status: 204 }]);
    const first = await run(ctx);
    assert.equal(first.code, 9, first.out);
    assert.match(first.stderr, /rerun with --resume-build 5/);
    assert.deepEqual(summaryOf(first).groups, [{ name: "Group A", status: "skipped-all-builds" }, { name: "Group B", status: "failed" }]);
    const xbBefore = stubRecords(ctx).length;
    const gateRuns = fs.readFileSync(ctx.marker, "utf8").trim().split("\n").length;
    const second = await run(ctx, ["--resume-build", "5"]);
    assert.equal(second.code, 0, second.out);
    assert.equal(stubRecords(ctx).length, xbBefore, "resume runs no xcodebuild");
    assert.equal(fs.readFileSync(ctx.marker, "utf8").trim().split("\n").length, gateRuns, "resume runs no gates");
    const s = summaryOf(second);
    assert.equal(s.mode, "resume");
    assert.equal(s.build, 5);
    assert.equal(s.groups[1].status, "added");
  });

  it("preflight: a dirty tree exits 5; --allow-dirty passes", async (t) => {
    const ctx = await setup(t);
    fs.writeFileSync(path.join(ctx.repo, "scratch.txt"), "x");
    const r = await run(ctx);
    assert.equal(r.code, 5, r.out);
    assert.match(r.stderr, /dirty/);
    const ok = await run(ctx, ["--allow-dirty"]);
    assert.equal(ok.code, 0, ok.out);
  });

  it("preflight: buildReadyCheck that fails exits 5", async (t) => {
    const ctx = await setup(t, { config: { buildReadyCheck: ["false"] } });
    const r = await run(ctx);
    assert.equal(r.code, 5, r.out);
    assert.equal(byKind(ctx, "archive").length, 0);
  });

  it("preflight: buildReadyCheck receives the 40-hex HEAD sha as its last argument", async (t) => {
    const ctx = await setup(t);
    writeConfig(ctx, { ...baseConfig(), buildReadyCheck: ["/bin/sh", "-c", 'printf %s "$1" > "$0"', ctx.marker] });
    commitAll(ctx);
    const head = git(ctx.repo, "rev-parse", "HEAD");
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    const got = fs.readFileSync(ctx.marker, "utf8");
    assert.match(got, /^[0-9a-f]{40}$/);
    assert.equal(got, head);
    assert.equal(summaryOf(r).commit, head);
  });

  it("gates: a failing gate exits 5 naming its index and no archive runs", async (t) => {
    const ctx = await setup(t, { config: { gates: [["true"], ["false"]] } });
    const r = await run(ctx);
    assert.equal(r.code, 5, r.out);
    assert.match(r.stderr, /gate 1 failed/);
    assert.equal(byKind(ctx, "archive").length, 0);
  });

  it("gates: a cwd object runs in that directory, gates run in order before archive", async (t) => {
    const ctx = await setup(t, {
      config: (c, x) => ({
        ...c,
        gates: [["/bin/sh", "-c", 'echo first >> "$0"', x.marker], { cwd: "sub", argv: ["/bin/sh", "-c", 'pwd -P >> "$0"', x.marker] }],
      }),
    });
    const r = await run(ctx);
    assert.equal(r.code, 0, r.out);
    const lines = fs.readFileSync(ctx.marker, "utf8").trim().split("\n");
    assert.equal(lines[0], "first");
    assert.ok(lines[1].endsWith("/repo/sub"), lines[1]);
  });

  it("gates: not run on --dry-run or --resume-build", async (t) => {
    const ctx = await setup(t, { config: (c, x) => ({ ...c, gates: [["/bin/sh", "-c", 'touch "$0"', x.marker]] }) });
    assert.equal((await run(ctx, ["--dry-run"])).code, 0);
    assert.equal((await run(ctx, ["--resume-build", "5"])).code, 0);
    assert.ok(!fs.existsSync(ctx.marker));
  });

  it("gates: a cwd escaping the build dir exits 3", async (t) => {
    const ctx = await setup(t, { config: { gates: [{ cwd: "../outside", argv: ["true"] }] } });
    const r = await run(ctx);
    assert.equal(r.code, 3, r.out);
    assert.match(r.stderr, /gates\[0\]\.cwd/);
  });

  it("forbiddenWorktrees containing the build dir exits 5", async (t) => {
    const ctx = await setup(t);
    writeConfig(ctx, { ...baseConfig(), forbiddenWorktrees: [fs.realpathSync(ctx.repo)] });
    commitAll(ctx);
    const r = await run(ctx);
    assert.equal(r.code, 5, r.out);
    assert.match(r.stderr, /forbidden worktree/);
    assert.equal(byKind(ctx, "archive").length, 0);
  });

  it("defaultBranch at another commit without worktreeBase exits 5", async (t) => {
    const ctx = await setup(t, { secondCommit: true, config: { defaultBranch: "other" } });
    git(ctx.repo, "branch", "other", "HEAD~1");
    const r = await run(ctx);
    assert.equal(r.code, 5, r.out);
    assert.match(r.stderr, /checkout is not at other/);
  });

  for (const outcome of ["success", "failure"]) {
    it(`worktreeBase: the build runs in a new worktree that is gone after a ${outcome}`, async (t) => {
      const ctx = await setup(t, { secondCommit: true });
      const base = path.join(ctx.root, "wt");
      writeConfig(ctx, {
        ...baseConfig(),
        worktreeBase: base,
        defaultBranch: "first",
        gates: [["/bin/sh", "-c", 'pwd -P > "$0"', ctx.marker], ...(outcome === "failure" ? [["false"]] : [])],
      });
      commitAll(ctx);
      git(ctx.repo, "branch", "first", "HEAD~2");
      // the worktree is built from the commit "first", which has no config changes; the config is read from the checkout
      const r = await run(ctx);
      assert.equal(r.code, outcome === "success" ? 0 : 5, r.out);
      const used = fs.readFileSync(ctx.marker, "utf8").trim();
      assert.ok(used.startsWith(fs.realpathSync(base)), used);
      assert.notEqual(used, fs.realpathSync(ctx.repo));
      assert.ok(!fs.existsSync(used), "the worktree is removed");
      assert.equal(fs.readdirSync(base).length, 0);
      assert.ok(!git(ctx.repo, "worktree", "list").includes("hivemill-ios-release"));
    });
  }

  it("notes.path expands ~/ and {build}", async (t) => {
    const ctx = await setup(t, { config: { notes: { path: "~/notes/n-{build}.md" } } });
    fs.mkdirSync(path.join(ctx.home, "notes"));
    fs.writeFileSync(path.join(ctx.home, "notes", "n-5.md"), "Try the new screen.\n");
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 0, r.out);
    assert.match(r.stderr, /notes: 20 characters/);
  });

  it("marketingVersion file+regex: a match gives the version", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 0, r.out);
    assert.equal(summaryOf(r).marketingVersion, "1.2.3");
    assert.deepEqual(ctx.mock.requests.find((x) => x.path === "/v1/builds").query["filter[preReleaseVersion.version]"], "1.2.3");
  });

  it("marketingVersion file+regex: no match exits 5", async (t) => {
    const ctx = await setup(t, { config: { marketingVersion: { file: "ios/Config/Version.xcconfig", regex: "^NOPE = (.+)$" } } });
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 5, r.out);
  });

  it("marketingVersion file+regex: a regex that does not compile exits 3", async (t) => {
    const ctx = await setup(t, { config: { marketingVersion: { file: "ios/Config/Version.xcconfig", regex: "(" } } });
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 3, r.out);
    assert.match(r.stderr, /marketingVersion\.regex/);
  });

  it("marketingVersion buildSetting: read from xcodebuild -showBuildSettings under the reset PATH", async (t) => {
    const ctx = await setup(t, { config: { marketingVersion: { buildSetting: "MARKETING_VERSION" } } });
    const r = await run(ctx, ["--dry-run"], { STUB_MARKETING_VERSION: "3.4" });
    assert.equal(r.code, 0, r.out);
    assert.equal(summaryOf(r).marketingVersion, "3.4");
    const rec = byKind(ctx, "-showBuildSettings")[0];
    assert.equal(rec.path, RESET);
  });

  it("config: heavySlot exits 3 naming the key", async (t) => {
    const ctx = await setup(t, { config: { heavySlot: ["nice"] } });
    const r = await run(ctx);
    assert.equal(r.code, 3, r.out);
    assert.match(r.stderr, /heavySlot/);
  });

  it("config: schemaVersion absent is fine and 2 exits 3", async (t) => {
    const ctx = await setup(t);
    assert.equal((await run(ctx, ["--dry-run"])).code, 0);
    writeConfig(ctx, { ...baseConfig(), schemaVersion: 2 });
    commitAll(ctx);
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 3, r.out);
    assert.match(r.stderr, /schemaVersion/);
  });

  it("config: submitExternal \"yes\" exits 3", async (t) => {
    const ctx = await setup(t, { config: { submitExternal: "yes" } });
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 3, r.out);
    assert.match(r.stderr, /submitExternal/);
  });

  it("config: a #913-shaped placeholder config with every field passes a dry run", async (t) => {
    const ctx = await setup(t, {
      config: (c, x) => ({
        ascProfile: "default",
        project: "ios/MyApp.xcodeproj",
        scheme: "MyApp",
        configuration: "Release",
        bundleId: "com.example.myapp",
        teamId: "ABCDE12345",
        signing: "xcodeAccount",
        marketingVersion: { file: "ios/Config/Version.xcconfig", regex: "^MARKETING_VERSION = (.+)$" },
        testerGroup: ["Internal Testers"],
        submitExternal: false,
        notes: { path: "docs/notes-{build}.md" },
        notesLocale: "en-US",
        buildReadyCheck: ["true"],
        gates: [["true"], { cwd: "sub", argv: ["true"] }],
        worktreeBase: path.join(x.root, "wt"),
        forbiddenWorktrees: [path.join(x.root, "elsewhere")],
        defaultBranch: "main",
        heavySlotCommand: ["nice"],
        pollTimeoutMinutes: 60,
        pollIntervalSeconds: 0,
      }),
    });
    const r = await run(ctx, ["--dry-run"]);
    assert.equal(r.code, 0, r.out);
  });

  it("usage: an unknown flag exits 2", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, ["--frobnicate"]);
    assert.equal(r.code, 2, r.out);
  });

  it("usage: --signing bogus exits 2", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, ["--signing", "bogus"]);
    assert.equal(r.code, 2, r.out);
  });

  it("usage: a disallowed HIVEMILL_ASC_API_BASE exits 2", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, ["--dry-run"], { HIVEMILL_ASC_API_BASE: "https://evil.example.com" });
    assert.equal(r.code, 2, r.out);
  });

  it("usage: --help exits 0 and lists the exit codes", async (t) => {
    const ctx = await setup(t);
    const r = await run(ctx, ["--help"]);
    assert.equal(r.code, 0);
    assert.match(r.stdout, /--resume-build N/);
  });

  for (const [label, args, setupOpts, code] of [
    ["success", [], {}, 0],
    ["dry-run", ["--dry-run"], {}, 0],
    ["exit-5 run", [], { files: {} }, 5],
  ]) {
    it(`summary line: the last stdout line is the JSON summary for a ${label}`, async (t) => {
      const ctx = await setup(t, setupOpts);
      if (code === 5) fs.writeFileSync(path.join(ctx.repo, "dirty.txt"), "x");
      const r = await run(ctx, args);
      assert.equal(r.code, code, r.out);
      const s = summaryOf(r);
      assert.deepEqual(Object.keys(s), ["ok", "exitCode", "mode", "signing", "marketingVersion", "build", "buildId", "groups", "commit", "error"]);
      assert.equal(s.exitCode, r.code);
      assert.equal(s.ok, code === 0);
      assert.ok(Array.isArray(s.groups) && s.groups.every((g) => typeof g.name === "string" && typeof g.status === "string"));
      assert.equal(s.mode, label === "dry-run" ? "dry-run" : "release");
      if (code === 0) {
        assert.equal(s.error, null);
        assert.equal(s.build, 5);
        if (label === "success") assert.equal(s.buildId, "BLD1");
      } else {
        assert.match(s.error, /dirty/);
      }
      assert.equal(r.stdout.trimEnd().split("\n").length, 1, "stdout holds only the summary line");
    });
  }
});
