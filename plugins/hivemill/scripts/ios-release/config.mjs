// Config loading and validation for the ios-release scripts: <project>/.hivemill/ios-release.json.
// Node >= 20, stdlib only. Strict: an unknown key, a wrong type or a bad value exits 3 naming the field.
// Also holds the helpers release.sh needs so that bash never parses JSON, regexes or "~"/"{build}" itself.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";

export class ConfigError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "ConfigError";
    this.code = code;
  }
}
const bad = (field, why) => new ConfigError(3, `config: ${field}: ${why}`);

const KEYS = [
  "schemaVersion", "ascProfile", "workspace", "project", "scheme", "configuration", "bundleId", "teamId", "signing",
  "marketingVersion", "testerGroup", "submitExternal", "notes", "notesLocale", "buildReadyCheck", "gates", "worktreeBase",
  "forbiddenWorktrees", "defaultBranch", "heavySlotCommand", "pollTimeoutMinutes", "pollIntervalSeconds",
];
const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const isStr = (v) => typeof v === "string" && v !== "";

function str(cfg, k, def) {
  if (cfg[k] === undefined) return def;
  if (!isStr(cfg[k])) throw bad(k, "must be a non-empty string");
  return cfg[k];
}
function int(cfg, k, min, max, def) {
  if (cfg[k] === undefined) return def;
  if (!Number.isInteger(cfg[k])) throw bad(k, "must be an integer");
  if (cfg[k] < min || cfg[k] > max) throw bad(k, `must be between ${min} and ${max}`);
  return cfg[k];
}
function argv(v, field) {
  if (!Array.isArray(v) || v.length === 0 || !v.every(isStr)) throw bad(field, "must be a non-empty array of non-empty strings (an argv)");
  return [...v];
}
export function expandTilde(p, home, field) {
  if (p !== "~" && !p.startsWith("~/")) return p;
  if (!home) throw bad(field, "uses ~ but HOME is not set");
  return p === "~" ? home : path.join(home, p.slice(2));
}

function checkRegex(src, field) {
  let re;
  try {
    re = new RegExp(src, "m");
  } catch {
    throw bad(field, "is not a valid regular expression");
  }
  if (new RegExp(`${src}|`, "m").exec("").length < 2) throw bad(field, "needs at least one capture group");
  return re;
}

function marketingVersion(v) {
  if (!isObj(v)) throw bad("marketingVersion", "must be an object");
  const keys = Object.keys(v).sort().join(",");
  const forms = { "file,regex": "file", buildSetting: "buildSetting", command: "command", value: "value" };
  const form = forms[keys];
  if (!form) throw bad("marketingVersion", 'must have exactly one form: "file" with "regex", or "buildSetting", or "command", or "value"');
  if (form === "file") {
    if (!isStr(v.file)) throw bad("marketingVersion.file", "must be a non-empty string");
    if (!isStr(v.regex)) throw bad("marketingVersion.regex", "must be a non-empty string");
    checkRegex(v.regex, "marketingVersion.regex");
  } else if (form === "command") {
    argv(v.command, "marketingVersion.command");
  } else if (!isStr(v[form])) {
    throw bad(`marketingVersion.${form}`, "must be a non-empty string");
  }
  return { ...v };
}

function notes(v, home) {
  if (!isObj(v)) throw bad("notes", "must be an object");
  const keys = Object.keys(v);
  if (keys.length !== 1 || !["path", "command"].includes(keys[0])) throw bad("notes", 'must have exactly one key: "path" or "command"');
  if (keys[0] === "command") return { command: argv(v.command, "notes.command") };
  if (!isStr(v.path)) throw bad("notes.path", "must be a non-empty string");
  return { path: expandTilde(v.path, home, "notes.path") };
}

function gate(item, i) {
  const field = `gates[${i}]`;
  if (Array.isArray(item)) return { cwd: ".", argv: argv(item, field) };
  if (!isObj(item) || Object.keys(item).sort().join(",") !== "argv,cwd") throw bad(field, 'must be an argv array or an object with exactly the keys "cwd" and "argv"');
  if (!isStr(item.cwd)) throw bad(`${field}.cwd`, "must be a non-empty string");
  const norm = path.normalize(item.cwd);
  if (path.isAbsolute(norm) || norm === ".." || norm.startsWith(`..${path.sep}`)) throw bad(`${field}.cwd`, "must stay inside the build directory");
  return { cwd: norm, argv: argv(item.argv, `${field}.argv`) };
}

// Validates a parsed config object and returns it with the defaults applied. Throws ConfigError(3).
export function validate(cfg, { home = process.env.HOME } = {}) {
  if (!isObj(cfg)) throw bad("(root)", "must be a JSON object");
  for (const k of Object.keys(cfg)) {
    if (!KEYS.includes(k)) {
      throw bad(k, k === "heavySlot" ? 'unknown key (renamed: use "heavySlotCommand")' : "unknown key");
    }
  }
  const out = {};
  if (cfg.schemaVersion !== undefined && cfg.schemaVersion !== 1) throw bad("schemaVersion", "must be 1");
  out.schemaVersion = 1;
  out.ascProfile = str(cfg, "ascProfile", "default");
  if (!/^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$/.test(out.ascProfile) || out.ascProfile.includes("..")) {
    throw bad("ascProfile", "must be 1-64 characters of A-Z a-z 0-9 . _ - and not start with a dot");
  }
  const hasWs = cfg.workspace !== undefined;
  const hasPr = cfg.project !== undefined;
  if (hasWs === hasPr) throw bad("workspace/project", 'set exactly one of "workspace" or "project"');
  if (hasWs) {
    out.workspace = str(cfg, "workspace");
    if (!out.workspace.endsWith(".xcworkspace")) throw bad("workspace", "must end with .xcworkspace");
  } else {
    out.project = str(cfg, "project");
    if (!out.project.endsWith(".xcodeproj")) throw bad("project", "must end with .xcodeproj");
  }
  for (const k of ["scheme", "bundleId", "teamId"]) if (cfg[k] === undefined) throw bad(k, "is required");
  out.scheme = str(cfg, "scheme");
  out.configuration = str(cfg, "configuration", "Release");
  out.bundleId = str(cfg, "bundleId");
  if (!/^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/.test(out.bundleId)) throw bad("bundleId", "must be a reverse-DNS name such as com.example.app");
  out.teamId = str(cfg, "teamId");
  if (!/^[A-Z0-9]{10}$/.test(out.teamId)) throw bad("teamId", "must be 10 characters of A-Z 0-9");
  out.signing = str(cfg, "signing", "xcodeAccount");
  if (!["xcodeAccount", "apiKey"].includes(out.signing)) throw bad("signing", 'must be "xcodeAccount" or "apiKey"');
  if (cfg.marketingVersion === undefined) throw bad("marketingVersion", "is required");
  out.marketingVersion = marketingVersion(cfg.marketingVersion);
  const tg = cfg.testerGroup;
  if (tg === undefined) throw bad("testerGroup", "is required");
  const groups = typeof tg === "string" ? [tg] : tg;
  if (!Array.isArray(groups) || groups.length === 0 || !groups.every(isStr)) throw bad("testerGroup", "must be a group name or a non-empty array of group names");
  if (new Set(groups).size !== groups.length) throw bad("testerGroup", "group names must be distinct");
  out.testerGroup = [...groups];
  if (cfg.submitExternal !== undefined && typeof cfg.submitExternal !== "boolean") throw bad("submitExternal", "must be true or false");
  out.submitExternal = cfg.submitExternal ?? false;
  if (cfg.notes === undefined) throw bad("notes", "is required");
  out.notes = notes(cfg.notes, home);
  out.notesLocale = str(cfg, "notesLocale", "en-US");
  if (cfg.buildReadyCheck !== undefined) out.buildReadyCheck = argv(cfg.buildReadyCheck, "buildReadyCheck");
  if (cfg.gates !== undefined && !Array.isArray(cfg.gates)) throw bad("gates", "must be an array");
  out.gates = (cfg.gates ?? []).map(gate);
  if (cfg.worktreeBase !== undefined) out.worktreeBase = expandTilde(str(cfg, "worktreeBase"), home, "worktreeBase");
  if (cfg.forbiddenWorktrees !== undefined) {
    if (!Array.isArray(cfg.forbiddenWorktrees) || !cfg.forbiddenWorktrees.every(isStr)) throw bad("forbiddenWorktrees", "must be an array of directory paths");
    out.forbiddenWorktrees = cfg.forbiddenWorktrees.map((p) => expandTilde(p, home, "forbiddenWorktrees"));
  }
  if (cfg.defaultBranch !== undefined) {
    out.defaultBranch = str(cfg, "defaultBranch");
    if (/\s/.test(out.defaultBranch) || out.defaultBranch.startsWith("-")) throw bad("defaultBranch", "must be a git ref without spaces, not starting with '-'");
  }
  if (cfg.heavySlotCommand !== undefined) out.heavySlotCommand = argv(cfg.heavySlotCommand, "heavySlotCommand");
  out.pollTimeoutMinutes = int(cfg, "pollTimeoutMinutes", 1, 180, 60);
  out.pollIntervalSeconds = int(cfg, "pollIntervalSeconds", 0, 600, 30);
  return out;
}

export function loadConfig(file, opts) {
  let text;
  try {
    text = fs.readFileSync(file, "utf8");
  } catch {
    throw new ConfigError(3, `config: cannot read ${file}`);
  }
  let json;
  try {
    json = JSON.parse(text);
  } catch {
    throw new ConfigError(3, `config: ${file} is not valid JSON`);
  }
  return validate(json, opts);
}

// ---- marketing version, notes path, forbidden worktrees ----
const VERSION = /^\d+(\.\d+){0,2}$/;
const fail5 = (msg) => new ConfigError(5, msg);

export function parseBuildSetting(text, name) {
  const re = new RegExp(`^\\s*${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")} = (.*)$`, "m");
  const m = re.exec(text);
  return m ? m[1].trim() : null;
}

// Returns the marketing version string. buildSettingsText is only needed for the buildSetting form.
export function resolveMarketingVersion(cfg, buildDir, buildSettingsText = "") {
  const mv = cfg.marketingVersion;
  let v = null;
  if (mv.value !== undefined) {
    v = mv.value;
  } else if (mv.file !== undefined) {
    let text;
    try {
      text = fs.readFileSync(path.resolve(buildDir, mv.file), "utf8");
    } catch {
      throw fail5(`marketing version: cannot read ${mv.file} in the build directory`);
    }
    const m = new RegExp(mv.regex, "m").exec(text);
    if (!m) throw fail5(`marketing version: the regex found no match in ${mv.file}`);
    v = m[1];
  } else if (mv.buildSetting !== undefined) {
    v = parseBuildSetting(buildSettingsText, mv.buildSetting);
    if (v === null) throw fail5(`marketing version: build setting ${mv.buildSetting} not found in the xcodebuild output`);
  } else {
    try {
      v = execFileSync(mv.command[0], mv.command.slice(1), { cwd: buildDir, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    } catch {
      throw fail5("marketing version: the marketingVersion command failed");
    }
  }
  v = String(v ?? "").trim();
  if (!VERSION.test(v)) throw fail5("marketing version: the result is not 1-3 dot-separated integers");
  return v;
}

export function notesPath(cfg, build, buildDir) {
  if (cfg.notes.path === undefined) throw new ConfigError(2, "notes uses a command, not a path");
  return path.resolve(buildDir, cfg.notes.path.split("{build}").join(String(build)));
}

const real = (p) => {
  try {
    return fs.realpathSync(p);
  } catch {
    return path.resolve(p);
  }
};
// Exit 5 when the build dir is equal to or inside a forbidden worktree.
export function checkForbidden(cfg, buildDir) {
  const b = real(buildDir);
  for (const f of cfg.forbiddenWorktrees ?? []) {
    const r = real(f);
    if (b === r || b.startsWith(r.endsWith(path.sep) ? r : r + path.sep)) throw fail5(`the build directory is inside a forbidden worktree (${f})`);
  }
}

// ---- CLI ----
const HELP = `usage: node config.mjs <command> --config PATH [...]
Validates .hivemill/ios-release.json (defaults applied) and prints values for release.sh. Exit 3 config error,
5 preflight (marketing version), 2 usage.
Commands:
  check --config PATH                      validate only; prints nothing
  get --config PATH FIELD                  print one value. Lists (arrays) are NUL-terminated items.
        FIELD: any config key (scalars as text, arrays as NUL lists; testerGroup is always a list; an unset optional
        field prints nothing), or marketingVersion.form (file|buildSetting|command|value), marketingVersion.buildSetting,
        notes.kind (path|command), notes.command (list), gate-count
  get-gate --config PATH --index I         print gate I: its cwd, then its argv, each NUL-terminated
  marketing-version --config PATH --build-dir DIR   print the marketing version; for the buildSetting form the
        xcodebuild -showBuildSettings output is read from stdin
  notes-path --config PATH --build N --build-dir DIR   print the notes file path ("~" expanded, "{build}" replaced)
  check-forbidden --config PATH --build-dir DIR        exit 5 when DIR is inside a forbiddenWorktrees entry
  --help`;

function flagsOf(args) {
  const f = {};
  const rest = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i].startsWith("--")) {
      if (i + 1 >= args.length) throw new ConfigError(2, `${args[i]} needs a value`);
      f[args[i].slice(2)] = args[++i];
    } else rest.push(args[i]);
  }
  return { f, rest };
}
const list = (items) => items.map((s) => `${s}\0`).join("");

function getField(cfg, field) {
  switch (field) {
    case "gate-count": return String(cfg.gates.length) + "\n";
    case "marketingVersion.form": return Object.keys(cfg.marketingVersion).includes("file") ? "file\n" : `${Object.keys(cfg.marketingVersion)[0]}\n`;
    case "marketingVersion.buildSetting": return cfg.marketingVersion.buildSetting === undefined ? "" : `${cfg.marketingVersion.buildSetting}\n`;
    case "notes.kind": return cfg.notes.path !== undefined ? "path\n" : "command\n";
    case "notes.command": return list(cfg.notes.command ?? []);
    case "notes": case "marketingVersion": case "gates": throw new ConfigError(2, `use a dotted field or a helper command for ${field}`);
    default:
      if (!KEYS.includes(field)) throw new ConfigError(2, `unknown field: ${field}`);
      if (cfg[field] === undefined) return "";
      return Array.isArray(cfg[field]) ? list(cfg[field]) : `${cfg[field]}\n`;
  }
}

export function runCli(args, { stdin = () => fs.readFileSync(0, "utf8") } = {}) {
  const [cmd, ...rest] = args;
  if (cmd === "--help" || cmd === "-h" || cmd === undefined) return HELP + "\n";
  const { f, rest: pos } = flagsOf(rest);
  if (!f.config) throw new ConfigError(2, "--config PATH is required");
  const cfg = loadConfig(f.config);
  switch (cmd) {
    case "check": return "";
    case "get":
      if (pos.length !== 1) throw new ConfigError(2, "get needs exactly one FIELD");
      return getField(cfg, pos[0]);
    case "get-gate": {
      const g = cfg.gates[Number(f.index)];
      if (!/^\d+$/.test(f.index ?? "") || !g) throw new ConfigError(2, "get-gate needs a valid --index");
      return list([g.cwd, ...g.argv]);
    }
    case "marketing-version":
      return resolveMarketingVersion(cfg, f["build-dir"] ?? ".", cfg.marketingVersion.buildSetting === undefined ? "" : stdin()) + "\n";
    case "notes-path":
      if (!/^\d+$/.test(f.build ?? "")) throw new ConfigError(2, "notes-path needs --build N");
      return notesPath(cfg, f.build, f["build-dir"] ?? ".") + "\n";
    case "check-forbidden":
      checkForbidden(cfg, f["build-dir"] ?? ".");
      return "";
    default:
      throw new ConfigError(2, `unknown command: ${cmd}`);
  }
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
    process.stdout.write(runCli(process.argv.slice(2)));
  } catch (err) {
    if (err instanceof ConfigError) {
      process.stderr.write(`${err.message}\n`);
      process.exit(err.code);
    }
    process.stderr.write("config: unexpected internal error\n");
    process.exit(1);
  }
}
