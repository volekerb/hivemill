import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { redact } from "../redact.mjs";

const REDACT = fileURLToPath(new URL("../redact.mjs", import.meta.url));
const JWT = "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.c2lnbmF0dXJl";
const BEGIN = "-----BEGIN " + "PRIVATE KEY-----";
const END = "-----END " + "PRIVATE KEY-----";
const PEM = `${BEGIN}\nMIGHAgEAMBMGByqGSM49\nAgEGCCqGSM49AwEH\n${END}`;
const filter = (input, values = []) =>
  spawnSync("node", [REDACT], { input, encoding: "utf8", env: { PATH: process.env.PATH, HIVEMILL_REDACT_VALUES: values.join("\n") } });

describe("redact()", () => {
  it("replaces each value, every occurrence", () => {
    assert.equal(redact("id=KEY123 again KEY123", ["KEY123"]), "id=[REDACTED] again [REDACTED]");
  });
  it("replaces the key file base name when the value is a path, and the whole path first", () => {
    const out = redact("path /k/AuthKey_X1.p8 file AuthKey_X1.p8", ["/k/AuthKey_X1.p8"]);
    assert.equal(out, "path [REDACTED] file [REDACTED]");
  });
  it("replaces a JWT", () => {
    assert.equal(redact(`Authorization: Bearer ${JWT}`), "Authorization: Bearer [REDACTED]");
  });
  it("replaces a multi-line PEM private key block, also an unterminated one", () => {
    assert.equal(redact(`before\n${PEM}\nafter`), "before\n[REDACTED]\nafter");
    assert.ok(!redact(`x\n${BEGIN}\nAAAA`).includes("AAAA"));
  });
  it("ignores empty values and leaves clean text alone", () => {
    assert.equal(redact("nothing here", ["", undefined]), "nothing here");
  });
});

describe("redact.mjs stdin filter", () => {
  it("filters line by line using HIVEMILL_REDACT_VALUES", () => {
    const r = filter("one KEY123\ntwo\nthree /k/AuthKey_X1.p8", ["KEY123", "/k/AuthKey_X1.p8"]);
    assert.equal(r.status, 0);
    assert.equal(r.stdout, "one [REDACTED]\ntwo\nthree [REDACTED]");
  });
  it("swallows a PEM block that spans lines and keeps the text around it", () => {
    const r = filter(`a\n${PEM}\nb\n`);
    assert.equal(r.stdout, "a\n[REDACTED]\nb\n");
  });
  it("redacts a JWT in the stream", () => {
    assert.equal(filter(`token ${JWT}\n`).stdout, "token [REDACTED]\n");
  });
  it("has no off switch: an env var named like one changes nothing", () => {
    const r = spawnSync("node", [REDACT], { input: `${JWT}\n`, encoding: "utf8", env: { PATH: process.env.PATH, HIVEMILL_REDACT: "off", HIVEMILL_NO_REDACT: "1" } });
    assert.equal(r.stdout, "[REDACTED]\n");
  });
});
