// Redaction for the ios-release scripts. Node >= 20, stdlib only. There is no switch to turn it off.
//
// redact(text, values): replaces every non-empty value, the base name of every value that is a path, any JWT and any
// PEM private-key block with "[REDACTED]".
// CLI: "node redact.mjs" copies stdin to stdout line by line; the values come from env HIVEMILL_REDACT_VALUES
// (newline-separated).
import path from "node:path";
import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const MASK = "[REDACTED]";
const JWT = /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*/g;
const PEM_BLOCK = /-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*$/g;
const PEM_BEGIN = /-----BEGIN [A-Z ]*PRIVATE KEY-----/;
const PEM_END = /-----END [A-Z ]*PRIVATE KEY-----/;

function expand(values) {
  const all = new Set();
  for (const v of values ?? []) {
    if (typeof v !== "string" || v === "") continue;
    all.add(v);
    if (v.includes("/")) {
      const base = path.basename(v);
      if (base) all.add(base);
    }
  }
  return [...all].sort((a, b) => b.length - a.length); // longest first, so a path wins over its base name
}

export function redact(text, values = []) {
  let out = String(text).replace(PEM_BLOCK, MASK).replace(JWT, MASK);
  for (const v of expand(values)) out = out.split(v).join(MASK);
  return out;
}

// Line-by-line filter with the PEM state carried across lines. Returns null for a swallowed line.
export function createFilter(values = []) {
  let inPem = false;
  return (line) => {
    if (inPem) {
      if (PEM_END.test(line)) inPem = false;
      return null;
    }
    if (PEM_BEGIN.test(line)) {
      if (!PEM_END.test(line)) inPem = true;
      return MASK;
    }
    return redact(line, values);
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
  const values = (process.env.HIVEMILL_REDACT_VALUES ?? "").split("\n").filter(Boolean);
  const filter = createFilter(values);
  let buf = "";
  const emit = (line, nl) => {
    const r = filter(line);
    if (r !== null) process.stdout.write(r + (nl ? "\n" : ""));
  };
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => {
    buf += chunk;
    let i;
    while ((i = buf.indexOf("\n")) !== -1) {
      emit(buf.slice(0, i), true);
      buf = buf.slice(i + 1);
    }
  });
  process.stdin.on("end", () => {
    if (buf) emit(buf, false);
  });
}
