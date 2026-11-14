// Prints the one-line JSON summary that ends every release.sh run. Node >= 20, stdlib only.
// node summary.mjs --ok true|false --exit-code N --mode release|dry-run|resume [--signing S] [--marketing-version V]
//   [--build N] [--build-id ID] [--commit SHA] [--error-file F] [--groups-file F]
// The groups file holds NUL-terminated name, status pairs; the last status of a name wins. The error text is redacted
// with the values in env HIVEMILL_REDACT_VALUES (newline-separated). Never prints a credential value.
import fs from "node:fs";
import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { redact } from "./redact.mjs";

export function buildSummary(f, redactValues = []) {
  const orNull = (v) => (v === undefined || v === "" ? null : v);
  const read = (file) => {
    if (!file) return "";
    try {
      return fs.readFileSync(file, "utf8");
    } catch {
      return "";
    }
  };
  const groups = new Map();
  const parts = read(f["groups-file"]).split("\0");
  parts.pop(); // text after the last NUL
  for (let i = 0; i + 1 < parts.length; i += 2) groups.set(parts[i], parts[i + 1]);
  const err = redact(read(f["error-file"]).split("\n")[0].trim(), redactValues).slice(0, 500);
  const build = orNull(f.build);
  return {
    ok: f.ok === "true",
    exitCode: Number(f["exit-code"]),
    mode: f.mode,
    signing: orNull(f.signing),
    marketingVersion: orNull(f["marketing-version"]),
    build: build === null ? null : Number(build),
    buildId: orNull(f["build-id"]),
    groups: [...groups].map(([name, status]) => ({ name, status })),
    commit: orNull(f.commit),
    error: err === "" ? null : err,
  };
}

function isMain() {
  try {
    return process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isMain()) {
  const a = process.argv.slice(2);
  const f = {};
  for (let i = 0; i + 1 < a.length; i += 2) f[a[i].replace(/^--/, "")] = a[i + 1];
  const values = (process.env.HIVEMILL_REDACT_VALUES ?? "").split("\n").filter(Boolean);
  process.stdout.write(JSON.stringify(buildSummary(f, values)) + "\n");
}
