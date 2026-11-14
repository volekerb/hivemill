import { describe, it } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { ConfigError, validate } from "../config.mjs";

const CONFIG = fileURLToPath(new URL("../config.mjs", import.meta.url));
const EXAMPLE = fileURLToPath(new URL("../ios-release.example.json", import.meta.url));
const base = () => ({
  project: "ios/MyApp.xcodeproj",
  scheme: "MyApp",
  bundleId: "com.example.myapp",
  teamId: "ABCDE12345",
  marketingVersion: { value: "1.0" },
  testerGroup: "Internal Testers",
  notes: { path: "docs/n.md" },
});
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), "ios-config-test-"));
const cli = (args, input) => spawnSync("node", [CONFIG, ...args], { encoding: "utf8", input, env: { PATH: process.env.PATH, HOME: "/h" } });
const writeCfg = (obj) => {
  const f = path.join(tmp(), "c.json");
  fs.writeFileSync(f, typeof obj === "string" ? obj : JSON.stringify(obj));
  return f;
};
const rejects = (cfg, field) =>
  assert.throws(() => validate(cfg, { home: "/h" }), (e) => e instanceof ConfigError && e.code === 3 && e.message.includes(field), field);

describe("config.mjs validation (every rejection is exit 3 and names the field)", () => {
  it("accepts a minimal config and applies the defaults", () => {
    const c = validate(base(), { home: "/h" });
    assert.deepEqual(
      [c.schemaVersion, c.ascProfile, c.configuration, c.signing, c.submitExternal, c.notesLocale, c.gates, c.pollTimeoutMinutes, c.pollIntervalSeconds],
      [1, "default", "Release", "xcodeAccount", false, "en-US", [], 60, 30],
    );
    assert.deepEqual(c.testerGroup, ["Internal Testers"]);
  });

  it("rejects an unknown key, and tells heavySlot users about the rename", () => {
    rejects({ ...base(), bogus: 1 }, "bogus");
    rejects({ ...base(), heavySlot: ["nice"] }, "heavySlotCommand");
  });

  const cases = [
    ["wrong type: scheme", { scheme: 3 }, "scheme"],
    ["wrong type: pollTimeoutMinutes", { pollTimeoutMinutes: "60" }, "pollTimeoutMinutes"],
    ["pollIntervalSeconds out of range", { pollIntervalSeconds: 601 }, "pollIntervalSeconds"],
    ["both workspace and project", { workspace: "a.xcworkspace" }, "workspace/project"],
    ["neither workspace nor project", { project: undefined }, "workspace/project"],
    ["bad teamId", { teamId: "abc" }, "teamId"],
    ["schemaVersion 2", { schemaVersion: 2 }, "schemaVersion"],
    ["signing other than the two modes", { signing: "cloud" }, "signing"],
    ["submitExternal not a boolean", { submitExternal: "yes" }, "submitExternal"],
    ["marketingVersion with two forms", { marketingVersion: { value: "1", buildSetting: "X" } }, "marketingVersion"],
    ["marketingVersion file without regex", { marketingVersion: { file: "a" } }, "marketingVersion"],
    ["marketingVersion regex that does not compile", { marketingVersion: { file: "a", regex: "(" } }, "marketingVersion.regex"],
    ["marketingVersion regex without a capture group", { marketingVersion: { file: "a", regex: "x+" } }, "marketingVersion.regex"],
    ["notes with both path and command", { notes: { path: "a", command: ["b"] } }, "notes"],
    ["notes with neither", { notes: {} }, "notes"],
    ["gates item that is a string", { gates: ["make test"] }, "gates[0]"],
    ["gates object with an extra key", { gates: [{ cwd: "a", argv: ["x"], env: {} }] }, "gates[0]"],
    ["gate cwd escaping the build dir", { gates: [{ cwd: "../x", argv: ["x"] }] }, "gates[0].cwd"],
    ["gate cwd that is absolute", { gates: [{ cwd: "/tmp", argv: ["x"] }] }, "gates[0].cwd"],
    ["testerGroup empty array", { testerGroup: [] }, "testerGroup"],
    ["testerGroup with duplicates", { testerGroup: ["A", "A"] }, "testerGroup"],
    ["testerGroup with a non-string", { testerGroup: ["A", 3] }, "testerGroup"],
    ["testerGroup entry with an unknown key", { testerGroup: [{ id: "g1", extra: 1 }] }, "testerGroup entry 0"],
    ["testerGroup unknown key without id", { testerGroup: ["A", { name: "B", internal: true, extra: 1 }] }, "testerGroup entry 1"],
    ["testerGroup name without internal", { testerGroup: [{ name: "A" }] }, "testerGroup entry 0"],
    ["testerGroup internal without name", { testerGroup: [{ internal: true }] }, "testerGroup entry 0"],
    ["testerGroup id with another key", { testerGroup: [{ id: "g1", name: "A" }] }, "testerGroup entry 0"],
    ["testerGroup internal not a boolean", { testerGroup: [{ name: "A", internal: "true" }] }, "testerGroup entry 0"],
    ["testerGroup empty id", { testerGroup: [{ id: "" }] }, "testerGroup entry 0"],
    ["testerGroup id with a bad character", { testerGroup: [{ id: "a b" }] }, "testerGroup entry 0"],
    ["testerGroup id longer than 64", { testerGroup: [{ id: "a".repeat(65) }] }, "testerGroup entry 0"],
    ["testerGroup empty name", { testerGroup: [{ name: "", internal: true }] }, "testerGroup entry 0"],
    ["testerGroup empty string entry", { testerGroup: [""] }, "testerGroup entry 0"],
    ["testerGroup duplicate id", { testerGroup: [{ id: "g1" }, { id: "g1" }] }, "duplicate"],
    ["testerGroup duplicate name+internal", { testerGroup: [{ name: "A", internal: true }, { name: "A", internal: true }] }, "duplicate"],
    ["forbiddenWorktrees with a relative entry", { forbiddenWorktrees: ["../x"] }, "forbiddenWorktrees"],
    ["bundleId that is not reverse-DNS", { bundleId: "nodots" }, "bundleId"],
    ["workspace without the .xcworkspace suffix", { project: undefined, workspace: "a.xcodeproj" }, "workspace"],
    ["buildReadyCheck as a string", { buildReadyCheck: "./check.sh" }, "buildReadyCheck"],
    ["ascProfile with a slash", { ascProfile: "a/b" }, "ascProfile"],
    ["defaultBranch starting with a dash", { defaultBranch: "--upload-pack=x" }, "defaultBranch"],
  ];
  for (const [name, patch, field] of cases) {
    it(`exit 3: ${name}`, () => {
      const cfg = { ...base(), ...patch };
      for (const k of Object.keys(cfg)) if (cfg[k] === undefined) delete cfg[k];
      rejects(cfg, field);
    });
  }

  it("accepts a testerGroup list, both gate shapes, and the #913-shaped fields", () => {
    const c = validate({ ...base(), testerGroup: ["A", "B"], gates: [["make"], { cwd: "sub/dir", argv: ["swift", "test"] }], submitExternal: true }, { home: "/h" });
    assert.deepEqual(c.gates, [{ cwd: ".", argv: ["make"] }, { cwd: "sub/dir", argv: ["swift", "test"] }]);
    assert.equal(c.submitExternal, true);
  });

  it("~ is expanded only in notes.path, worktreeBase and forbiddenWorktrees", () => {
    const c = validate({ ...base(), notes: { path: "~/n/{build}.md" }, worktreeBase: "~/wt", forbiddenWorktrees: ["~", "~/x"], scheme: "~/s" }, { home: "/h" });
    assert.equal(c.notes.path, "/h/n/{build}.md");
    assert.equal(c.worktreeBase, "/h/wt");
    assert.deepEqual(c.forbiddenWorktrees, ["/h", "/h/x"]);
    assert.equal(c.scheme, "~/s");
  });
});

describe("config.mjs CLI", () => {
  it("check passes on the shipped example and prints nothing", () => {
    const r = cli(["check", "--config", EXAMPLE]);
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout, "");
  });

  it("check exits 3 on invalid JSON and on a missing file", () => {
    assert.equal(cli(["check", "--config", writeCfg("{")]).status, 3);
    assert.equal(cli(["check", "--config", "/nonexistent/c.json"]).status, 3);
  });

  it("get prints scalars with defaults and NUL-separated lists; testerGroup is always a list", () => {
    const f = writeCfg(base());
    assert.equal(cli(["get", "--config", f, "signing"]).stdout, "xcodeAccount\n");
    assert.equal(cli(["get", "--config", f, "pollTimeoutMinutes"]).stdout, "60\n");
    assert.equal(cli(["get", "--config", f, "testerGroup"]).stdout, "Internal Testers\0");
    const g = writeCfg({ ...base(), testerGroup: ["A", "B"] });
    assert.equal(cli(["get", "--config", g, "testerGroup"]).stdout, "A\0B\0");
    assert.equal(cli(["get", "--config", f, "worktreeBase"]).stdout, "");
    assert.equal(cli(["get", "--config", f, "nope"]).status, 2);
  });

  it("gate-count and get-gate print the cwd then the argv, NUL-separated", () => {
    const f = writeCfg({ ...base(), gates: [["make", "test"], { cwd: "ios/Kit", argv: ["swift", "test"] }] });
    assert.equal(cli(["get", "--config", f, "gate-count"]).stdout, "2\n");
    assert.equal(cli(["get-gate", "--config", f, "--index", "0"]).stdout, ".\0make\0test\0");
    assert.equal(cli(["get-gate", "--config", f, "--index", "1"]).stdout, "ios/Kit\0swift\0test\0");
    assert.equal(cli(["get-gate", "--config", f, "--index", "2"]).status, 2);
  });

  it("marketing-version: file+regex, value, command, and build settings from stdin", () => {
    const dir = tmp();
    fs.writeFileSync(path.join(dir, "v.xcconfig"), "A = 1\nMARKETING_VERSION = 2.5.1\n");
    const mv = (m, input) => cli(["marketing-version", "--config", writeCfg({ ...base(), marketingVersion: m }), "--build-dir", dir], input);
    assert.equal(mv({ file: "v.xcconfig", regex: "^MARKETING_VERSION = (.+)$" }).stdout, "2.5.1\n");
    assert.equal(mv({ file: "v.xcconfig", regex: "^NOPE = (.+)$" }).status, 5);
    assert.equal(mv({ value: "7.1" }).stdout, "7.1\n");
    assert.equal(mv({ value: "seven" }).status, 5);
    assert.equal(mv({ command: ["/bin/echo", "3.0"] }).stdout, "3.0\n");
    assert.equal(mv({ command: ["/bin/false"] }).status, 5);
    assert.equal(mv({ buildSetting: "MARKETING_VERSION" }, "Build settings:\n    MARKETING_VERSION = 4.2\n").stdout, "4.2\n");
    assert.equal(mv({ buildSetting: "MARKETING_VERSION" }, "nothing here\n").status, 5);
  });

  it("notes-path replaces every {build} and expands ~", () => {
    const f = writeCfg({ ...base(), notes: { path: "~/n/{build}/b{build}.md" } });
    const r = spawnSync("node", [CONFIG, "notes-path", "--config", f, "--build", "12", "--build-dir", "/b"], { encoding: "utf8", env: { PATH: process.env.PATH, HOME: "/h2" } });
    assert.equal(r.stdout, "/h2/n/12/b12.md\n");
  });

  it("check-forbidden exits 5 for the dir itself and a child, 0 for a sibling", () => {
    const root = fs.realpathSync(tmp());
    fs.mkdirSync(path.join(root, "a", "b"), { recursive: true });
    fs.mkdirSync(path.join(root, "other"));
    fs.mkdirSync(path.join(root, "a2"));
    const f = writeCfg({ ...base(), forbiddenWorktrees: [path.join(root, "a")] });
    const chk = (d) => cli(["check-forbidden", "--config", f, "--build-dir", d]).status;
    assert.equal(chk(path.join(root, "a")), 5);
    assert.equal(chk(path.join(root, "a", "b")), 5);
    assert.equal(chk(path.join(root, "other")), 0);
    assert.equal(chk(path.join(root, "a2")), 0, "a sibling sharing a name prefix is not inside");
  });

  it("--help documents the helper commands", () => {
    const out = execFileSync("node", [CONFIG, "--help"], { encoding: "utf8" });
    for (const w of ["gate-count", "get-gate", "marketing-version", "notes-path", "check-forbidden"]) assert.ok(out.includes(w), w);
  });

  describe("testerGroup entry forms", () => {
    const ok = (tg) => validate({ ...base(), testerGroup: tg }, { home: "/h" }).testerGroup;
    it("accepts a string, a list of strings, an id object, a name+internal object and a mixed list", () => {
      assert.deepEqual(ok("A"), ["A"]);
      assert.deepEqual(ok(["A", "B"]), ["A", "B"]);
      assert.deepEqual(ok({ id: "00000000-0000-0000-0000-000000000000" }), [{ id: "00000000-0000-0000-0000-000000000000" }]);
      assert.deepEqual(ok({ name: "A", internal: false }), [{ name: "A", internal: false }]);
      assert.equal(ok(["A", { id: "g-1" }, { name: "A", internal: true }]).length, 3);
    });
    it("allows a name string and a name+internal entry with the same name, and the same name with different internal", () => {
      assert.equal(ok(["A", { name: "A", internal: true }, { name: "A", internal: false }]).length, 3);
    });
    it("names the entry index for an invalid entry and says duplicate for a repeated selector", () => {
      assert.throws(() => ok(["A", { name: "B" }]), (e) => e.code === 3 && /testerGroup entry 1/.test(e.message));
      assert.throws(() => ok(["A", "A"]), (e) => e.code === 3 && /duplicate/.test(e.message));
    });
    it("get testerGroup.selectors prints kind, value, internal as NUL-terminated items per entry", () => {
      const f = writeCfg({ ...base(), testerGroup: ["Plain", { id: "g-1" }, { name: "Both", internal: true }, { name: "Both", internal: false }] });
      assert.equal(cli(["get", "--config", f, "testerGroup.selectors"]).stdout, "name\0Plain\0\0id\0g-1\0\0name-internal\0Both\0true\0name-internal\0Both\0false\0");
    });
    it("get testerGroup exits 2 when an entry is not a plain name", () => {
      const f = writeCfg({ ...base(), testerGroup: [{ id: "g-1" }] });
      assert.equal(cli(["get", "--config", f, "testerGroup"]).status, 2);
    });
  });
});
