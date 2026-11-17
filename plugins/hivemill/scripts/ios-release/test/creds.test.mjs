import { describe, it } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { makeFakeKey } from "./helpers/fakekey.mjs";

const CREDS = fileURLToPath(new URL("../creds.mjs", import.meta.url));
const STUBS = fileURLToPath(new URL("./stubs", import.meta.url));
const key = makeFakeKey();
const ID = "ID0000TEST";
const ISS = "11111111-2222-3333-4444-555555555555";

function home() {
  return fs.mkdtempSync(path.join(os.tmpdir(), "ios-creds-test-"));
}
function profile(h, text, mode = 0o600, name = "default") {
  const dir = path.join(h, ".config", "hivemill", "asc");
  fs.mkdirSync(dir, { recursive: true });
  const f = path.join(dir, `${name}.env`);
  fs.writeFileSync(f, text);
  fs.chmodSync(f, mode);
  return f;
}
const good = `ASC_KEY_ID=${ID}\nASC_ISSUER_ID=${ISS}\nASC_KEY_PATH=${key.keyPath}\n`;
// Runs the real CLI with a clean env: the stub security tool first on PATH, no inherited ASC_* variables.
function resolve(h, env = {}, args = ["--profile", "default"]) {
  return spawnSync("node", [CREDS, "resolve", ...args], {
    cwd: h,
    encoding: "utf8",
    env: { PATH: `${STUBS}:${path.dirname(process.execPath)}:/usr/bin:/bin`, HOME: h, ...env },
  });
}
const values = (r) => r.stdout.split("\0").slice(0, 3);

describe("creds.mjs resolve", () => {
  it("env: all three variables are used; stdout is NUL-separated and stderr names only the source", () => {
    const h = home();
    const r = resolve(h, { ASC_KEY_ID: ID, ASC_ISSUER_ID: ISS, ASC_KEY_PATH: key.keyPath });
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(values(r), [ID, ISS, key.keyPath]);
    assert.equal(r.stderr.trim(), "source: env");
  });

  it("env: a partial set exits 4 and names only the missing variable names", () => {
    const r = resolve(home(), { ASC_KEY_ID: ID });
    assert.equal(r.status, 4);
    assert.match(r.stderr, /ASC_ISSUER_ID, ASC_KEY_PATH/);
    assert.ok(!r.stderr.includes(ID));
  });

  it("env wins over a profile file", () => {
    const h = home();
    profile(h, good.replace(ID, "OTHERID000"));
    const r = resolve(h, { ASC_KEY_ID: ID, ASC_ISSUER_ID: ISS, ASC_KEY_PATH: key.keyPath });
    assert.deepEqual(values(r)[0], ID);
  });

  it("profile: a 0600 file works, ~/ in ASC_KEY_PATH becomes HOME, quotes and export are accepted", () => {
    const h = home();
    fs.copyFileSync(key.keyPath, path.join(h, "k.p8"));
    profile(h, `# comment\nexport ASC_KEY_ID="${ID}"\nASC_ISSUER_ID='${ISS}'\nASC_KEY_PATH=~/k.p8\nOTHER=ignored\n`);
    const r = resolve(h);
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(values(r), [ID, ISS, path.join(h, "k.p8")]);
    assert.equal(r.stderr.trim(), "source: profile:default");
  });

  for (const mode of [0o644, 0o640]) {
    it(`profile: mode ${mode.toString(8)} exits 4 with chmod 600 and never reaches the Keychain`, () => {
      const h = home();
      profile(h, good, mode);
      const log = path.join(h, "security.log");
      const r = resolve(h, { HIVEMILL_ASC_KEYCHAIN: "on", STUB_SECURITY_LOG: log, STUB_SECURITY_ASC_KEY_ID: "x" });
      assert.equal(r.status, 4);
      assert.match(r.stderr, /chmod 600/);
      assert.ok(!fs.existsSync(log), "the security stub was not called");
      assert.ok(!r.stderr.includes(ID));
    });
  }

  it("profile: a missing key name exits 4 naming the file and the name, not the values", () => {
    const h = home();
    profile(h, `ASC_KEY_ID=${ID}\nASC_KEY_PATH=${key.keyPath}\n`);
    const r = resolve(h);
    assert.equal(r.status, 4);
    assert.match(r.stderr, /ASC_ISSUER_ID/);
    assert.ok(!r.stderr.includes(ID));
  });

  it("profile: a value that looks like shell code is taken literally and nothing runs (no PWNED file)", () => {
    const h = home();
    const pwn = path.join(h, "PWNED");
    profile(h, `ASC_KEY_ID=$(touch ${pwn})\nASC_ISSUER_ID=\`touch ${pwn}\`\nASC_KEY_PATH=${key.keyPath}\n`);
    const r = resolve(h);
    assert.equal(r.status, 0, r.stderr);
    assert.equal(values(r)[0], `$(touch ${pwn})`);
    assert.equal(values(r)[1], `\`touch ${pwn}\``);
    assert.ok(!fs.existsSync(pwn), "the profile must be parsed, not sourced");
  });

  it("keychain: HIVEMILL_ASC_KEYCHAIN=on reads the three names through security", () => {
    const h = home();
    const log = path.join(h, "security.log");
    const r = resolve(h, { HIVEMILL_ASC_KEYCHAIN: "on", STUB_SECURITY_LOG: log, STUB_SECURITY_ASC_KEY_ID: ID, STUB_SECURITY_ASC_ISSUER_ID: ISS, STUB_SECURITY_ASC_KEY_PATH: key.keyPath });
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(values(r), [ID, ISS, key.keyPath]);
    assert.equal(r.stderr.trim(), "source: keychain:default");
    assert.match(fs.readFileSync(log, "utf8"), /find-generic-password -s hivemill-asc-default -a ASC_KEY_ID -w/);
  });

  it("keychain: on with nothing stored exits 4; off never calls security", () => {
    const h = home();
    const log = path.join(h, "security.log");
    assert.equal(resolve(h, { HIVEMILL_ASC_KEYCHAIN: "on", STUB_SECURITY_LOG: log }).status, 4);
    fs.rmSync(log, { force: true });
    const r = resolve(h, { HIVEMILL_ASC_KEYCHAIN: "off", STUB_SECURITY_LOG: log });
    assert.equal(r.status, 4);
    assert.ok(!fs.existsSync(log));
  });

  it("no credentials anywhere exits 4 and points at the README", () => {
    const r = resolve(home());
    assert.equal(r.status, 4);
    assert.match(r.stderr, /README/);
  });

  it("a key file that is not a P-256 key exits 4", () => {
    const h = home();
    const bad = path.join(h, "bad.p8");
    fs.writeFileSync(bad, "not a key");
    const r = resolve(h, { ASC_KEY_ID: ID, ASC_ISSUER_ID: ISS, ASC_KEY_PATH: bad });
    assert.equal(r.status, 4);
  });

  it("a bad profile name exits 2", () => {
    for (const p of ["../x", "a/b", ".hidden", "a..b", ""]) {
      assert.equal(resolve(home(), {}, ["--profile", p]).status, 2, p);
    }
  });

  it("usage errors exit 2", () => {
    assert.equal(resolve(home(), {}, []).status, 2);
  });
});
