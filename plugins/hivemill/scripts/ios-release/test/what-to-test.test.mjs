import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { convert } from "../what-to-test.mjs";

const SCRIPT = fileURLToPath(new URL("../what-to-test.mjs", import.meta.url));
const run = (input) => spawnSync(process.execPath, [SCRIPT], { input, encoding: "utf8" });

test("convert keeps only the What to Test section and strips markdown", () => {
  const out = convert("# Notes\n\n## What to Test\n\n- **Login** works\n- `Logout` works\n\n\n\n## Other\nignored\n");
  assert.equal(out, "- Login works\n- Logout works\n");
});

test("convert uses the whole text when there is no heading", () => {
  assert.equal(convert("just text\n"), "just text\n");
});

test("CLI converts stdin to stdout, exit 0", () => {
  const r = run("## What to Test\n- **a**\n");
  assert.equal(r.status, 0);
  assert.equal(r.stdout, "- a\n");
});

test("CLI exits 5 on empty input with a stderr message", () => {
  const r = run("  \n");
  assert.equal(r.status, 5);
  assert.equal(r.stdout, "");
  assert.match(r.stderr, /empty/);
});

test("CLI exits 5 when over 4000 characters", () => {
  const r = run("x".repeat(4001));
  assert.equal(r.status, 5);
  assert.match(r.stderr, /4000/);
});

test("CLI accepts exactly 4000 characters", () => {
  const r = run("x".repeat(3999));
  assert.equal(r.status, 0);
});
