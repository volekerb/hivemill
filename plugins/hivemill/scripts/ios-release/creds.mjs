// Credential resolution for the ios-release scripts. Node >= 20, stdlib only.
// Order: env ASC_KEY_ID/ASC_ISSUER_ID/ASC_KEY_PATH, then HOME/.config/hivemill/asc/<profile>.env (mode 600 enforced),
// then the macOS Keychain. The first source that HAS credentials wins; a present but broken source is exit 4,
// never a fall-through. The profile file is parsed, never sourced. Values are never printed except by "resolve" on stdout.
//
// CLI: node creds.mjs resolve --profile NAME
//   stdout: key id, issuer id, key path, each followed by a NUL. stderr: "source: env|profile:<name>|keychain:<name>".
//   Exit 2 bad profile name or usage, 4 credentials missing or unusable.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { AscError, loadKey } from "./asc.mjs";

const NAMES = ["ASC_KEY_ID", "ASC_ISSUER_ID", "ASC_KEY_PATH"];
const README = "see the ios-testflight-release README (Credentials)";

export function validateProfile(name) {
  if (typeof name !== "string" || !/^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$/.test(name) || name.includes("..")) {
    throw new AscError(2, "bad profile name: use 1-64 characters of A-Z a-z 0-9 . _ - (no leading dot, no '..', no '/')");
  }
  return name;
}

function expandHome(p, home) {
  if (p === "~") return home;
  return p.startsWith("~/") ? path.join(home, p.slice(2)) : p;
}

// Parses KEY=VALUE lines. Only the three ASC names are kept; nothing is evaluated.
export function parseEnvFile(text, file) {
  const out = {};
  text.split(/\r?\n/).forEach((raw, i) => {
    const line = raw.trim();
    if (!line || line.startsWith("#")) return;
    const m = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$/.exec(line);
    if (!m) throw new AscError(4, `${file}: line ${i + 1} is not KEY=VALUE`);
    let v = m[2].trim();
    if (v.length >= 2 && (v[0] === '"' || v[0] === "'") && v[v.length - 1] === v[0]) v = v.slice(1, -1);
    if (NAMES.includes(m[1])) out[m[1]] = v;
  });
  return out;
}

function fromProfileFile(file, home) {
  let st;
  try {
    st = fs.statSync(file);
  } catch (err) {
    if (err.code === "ENOENT") return null;
    throw new AscError(4, `cannot read ${file}`);
  }
  if (st.mode & 0o077) {
    throw new AscError(4, `refusing ${file}: permissions ${(st.mode & 0o777).toString(8).padStart(3, "0")}; run chmod 600 ${file}`);
  }
  const vals = parseEnvFile(fs.readFileSync(file, "utf8"), file);
  const missing = NAMES.filter((n) => !vals[n]);
  if (missing.length) throw new AscError(4, `${file} is missing: ${missing.join(", ")}`);
  return { ...vals, ASC_KEY_PATH: expandHome(vals.ASC_KEY_PATH, home) };
}

function fromKeychain(profile, env, platform, run) {
  const mode = env.HIVEMILL_ASC_KEYCHAIN ?? "auto";
  if (mode === "off" || (mode !== "on" && platform !== "darwin")) return null;
  const vals = {};
  for (const n of NAMES) {
    try {
      vals[n] = run("security", ["find-generic-password", "-s", `hivemill-asc-${profile}`, "-a", n, "-w"]).trim();
    } catch {
      if (mode === "on" || n !== NAMES[0]) throw new AscError(4, `the Keychain has no ${n} for service hivemill-asc-${profile}`);
      return null; // auto mode and nothing stored: this source has no credentials
    }
  }
  return vals;
}

export function resolveCreds({ profile, env = process.env, home = env.HOME, platform = process.platform, run } = {}) {
  validateProfile(profile);
  run ??= (cmd, args) => execFileSync(cmd, args, { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });
  let source = null;
  let vals = null;
  if (NAMES.some((n) => env[n])) {
    const missing = NAMES.filter((n) => !env[n]);
    if (missing.length) throw new AscError(4, `incomplete credentials in the environment; missing: ${missing.join(", ")}`);
    vals = { ASC_KEY_ID: env.ASC_KEY_ID, ASC_ISSUER_ID: env.ASC_ISSUER_ID, ASC_KEY_PATH: expandHome(env.ASC_KEY_PATH, home ?? "") };
    source = "env";
  }
  if (!vals) {
    if (!home) throw new AscError(4, "HOME is not set; cannot look for the credential profile");
    vals = fromProfileFile(path.join(home, ".config", "hivemill", "asc", `${profile}.env`), home);
    if (vals) source = `profile:${profile}`;
  }
  if (!vals) {
    vals = fromKeychain(profile, env, platform, run);
    if (vals) source = `keychain:${profile}`;
  }
  if (!vals) throw new AscError(4, `no App Store Connect credentials found for profile "${profile}"; ${README}`);
  loadKey(vals.ASC_KEY_PATH); // exists and is a P-256 key, else exit 4
  return { keyId: vals.ASC_KEY_ID, issuerId: vals.ASC_ISSUER_ID, keyPath: vals.ASC_KEY_PATH, source };
}

function isMain() {
  try {
    return process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isMain()) {
  try {
    const [cmd, flag, profile, ...extra] = process.argv.slice(2);
    if (cmd !== "resolve" || flag !== "--profile" || profile === undefined || extra.length) {
      throw new AscError(2, "usage: node creds.mjs resolve --profile NAME");
    }
    const c = resolveCreds({ profile });
    process.stderr.write(`source: ${c.source}\n`);
    process.stdout.write(`${c.keyId}\0${c.issuerId}\0${c.keyPath}\0`);
  } catch (err) {
    if (err instanceof AscError) {
      process.stderr.write(`creds: ${err.message}\n`);
      process.exit(err.code);
    }
    process.stderr.write("creds: unexpected internal error\n");
    process.exit(1);
  }
}
