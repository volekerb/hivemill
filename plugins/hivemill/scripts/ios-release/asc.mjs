// App Store Connect (ASC) helper for the hivemill iOS release skill. Node >= 20, stdlib only.
// Library (named exports) plus a CLI: node asc.mjs <cmd> [flags]. Exit codes follow the ios-release plan:
// 2 usage, 4 credentials, 6 ASC API error, 8 processing failed/timeout, 9 post-processing failed.
import crypto from "node:crypto";
import fs from "node:fs";
import { fileURLToPath } from "node:url";

export const DEFAULT_BASE_URL = "https://api.appstoreconnect.apple.com";
export const AUTH_MESSAGE = "ASC rejected the API key: check it is not revoked and has the App Manager role";
const AUDIENCE = "appstoreconnect-v1";
const TOKEN_LIFETIME_S = 1200;
const REMINT_MARGIN_S = 60;
const RETRY_SLEEPS_S = [1, 2, 4, 8, 16];
export const DEFAULT_TIMEOUT_MS = 30000;

export class AscError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "AscError";
    this.code = code;
  }
}

const b64url = (buf) => Buffer.from(buf).toString("base64url");

export function signJwt(keyId, issuerId, privateKeyPem, nowSeconds) {
  const header = { alg: "ES256", kid: keyId, typ: "JWT" };
  const payload = { iss: issuerId, iat: nowSeconds, exp: nowSeconds + TOKEN_LIFETIME_S, aud: AUDIENCE };
  const input = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(payload))}`;
  const sig = crypto.sign("sha256", Buffer.from(input), { key: privateKeyPem, dsaEncoding: "ieee-p1363" });
  return `${input}.${b64url(sig)}`;
}

// Returns the base URL to use, or throws AscError(2) when the override is not allowed.
export function resolveBaseUrl(override) {
  if (override === undefined || override === "") return DEFAULT_BASE_URL;
  if (override === DEFAULT_BASE_URL || /^http:\/\/(127\.0\.0\.1|localhost):\d{1,5}$/.test(override)) return override;
  throw new AscError(2, "HIVEMILL_ASC_API_BASE is not allowed: use the default, http://127.0.0.1:<port> or http://localhost:<port>");
}

export function loadKey(keyPath) {
  let pem;
  try {
    pem = fs.readFileSync(keyPath, "utf8");
  } catch {
    throw new AscError(4, "cannot read the ASC key file (ASC_KEY_PATH)");
  }
  try {
    const key = crypto.createPrivateKey(pem);
    if (key.asymmetricKeyType !== "ec" || key.asymmetricKeyDetails?.namedCurve !== "prime256v1") throw new Error("curve");
  } catch {
    throw new AscError(4, "the ASC key file is not a P-256 (prime256v1) private key");
  }
  return pem;
}

export function createClient({ keyId, issuerId, keyPem, now = Date.now, sleep, fetch: fetchImpl = globalThis.fetch, baseUrl = DEFAULT_BASE_URL, timeoutMs = DEFAULT_TIMEOUT_MS }) {
  sleep ??= (ms) => new Promise((r) => setTimeout(r, ms));
  const origin = new URL(baseUrl).origin;
  let token = null;
  let tokenExp = 0;

  function bearer() {
    const nowS = Math.floor(now() / 1000);
    if (!token || nowS >= tokenExp - REMINT_MARGIN_S) {
      token = signJwt(keyId, issuerId, keyPem, nowS);
      tokenExp = nowS + TOKEN_LIFETIME_S;
    }
    return token;
  }

  function buildUrl(pathOrUrl, query) {
    const url = new URL(pathOrUrl, baseUrl);
    if (url.origin !== origin) throw new AscError(6, "ASC returned a link to a different host; refusing to send credentials there");
    for (const [k, v] of Object.entries(query ?? {})) url.searchParams.set(k, String(v));
    return url;
  }

  // One logical request with retries on 429, 5xx and network errors. Returns parsed JSON (or null for 204).
  async function request(method, pathOrUrl, { query, body } = {}) {
    const url = buildUrl(pathOrUrl, query);
    for (let attempt = 0; ; attempt++) {
      let res = null;
      try {
        res = await fetchImpl(url.toString(), {
          method,
          headers: { Authorization: `Bearer ${bearer()}`, ...(body ? { "Content-Type": "application/json" } : {}) },
          body: body ? JSON.stringify(body) : undefined,
          signal: AbortSignal.timeout(timeoutMs),
        });
      } catch {
        res = null; // network error or timeout: retry below
      }
      if (res && (res.status === 401 || res.status === 403)) throw new AscError(6, AUTH_MESSAGE);
      const retryable = res === null || res.status === 429 || res.status >= 500;
      if (!retryable) {
        if (res.status >= 400) throw new AscError(6, `ASC returned HTTP ${res.status} for ${method} ${url.pathname}`);
        if (res.status === 204) return null;
        let text;
        try {
          text = await res.text();
        } catch {
          throw new AscError(6, `ASC response body for ${method} ${url.pathname} timed out or was cut off`);
        }
        if (!text) return null;
        try {
          return JSON.parse(text);
        } catch {
          throw new AscError(6, `ASC returned invalid JSON for ${method} ${url.pathname}`);
        }
      }
      if (attempt >= RETRY_SLEEPS_S.length) {
        throw new AscError(6, `ASC request ${method} ${url.pathname} failed after ${attempt} retries${res ? ` (HTTP ${res.status})` : " (network error)"}`);
      }
      await sleep(RETRY_SLEEPS_S[attempt] * 1000);
    }
  }

  async function getAll(path, query) {
    const items = [];
    let next = path;
    let q = query;
    while (next) {
      const page = await request("GET", next, { query: q });
      items.push(...(page?.data ?? []));
      next = page?.links?.next ?? null;
      q = undefined;
    }
    return items;
  }

  const rel = (type, id) => ({ data: { type, id } });

  return {
    request,

    async appId(bundleId) {
      const data = await getAll("/v1/apps", { "filter[bundleId]": bundleId });
      if (!data.length) throw new AscError(6, `no App Store Connect app found for bundle id ${bundleId}`);
      return data[0].id;
    },

    async nextBuildNumber(appId, version) {
      const data = await getAll("/v1/builds", { "filter[app]": appId, "filter[preReleaseVersion.version]": version, limit: 200 });
      let max = 0;
      for (const b of data) {
        const v = b.attributes?.version;
        if (!/^\d+$/.test(String(v))) throw new AscError(6, `existing build number "${v}" is not an integer; cannot compute the next one`);
        max = Math.max(max, Number(v));
      }
      return max + 1;
    },

    // sel: {id} | {name, internal?}. The filter[name] match is fuzzy on the ASC side, so only an exact
    // (case-sensitive) name counts; filter[id] results are also checked for an exact id.
    async groupInfo(appId, selector) {
      const sel = typeof selector === "string" ? { name: selector } : selector;
      let hits;
      let label;
      if (sel.id !== undefined) {
        label = `id ${sel.id}`;
        hits = (await getAll("/v1/betaGroups", { "filter[app]": appId, "filter[id]": sel.id })).filter((g) => g.id === sel.id);
      } else {
        label = sel["internal"] === undefined ? sel.name : `${sel.name} (internal ${sel["internal"]})`;
        hits = (await getAll("/v1/betaGroups", { "filter[app]": appId, "filter[name]": sel.name })).filter((g) => g.attributes?.name === sel.name);
        if (sel["internal"] !== undefined) hits = hits.filter((g) => (g.attributes?.isInternalGroup === true) === sel["internal"]);
      }
      if (hits.length === 0) throw new AscError(6, `beta group not found: ${label}`);
      if (hits.length > 1) {
        const ids = hits.map((g) => `${g.id} (isInternalGroup ${g.attributes?.isInternalGroup === true})`).join(", ");
        throw new AscError(6, `beta group name is ambiguous: ${sel.name}: ${ids}; set testerGroup to an object with id, or with name and internal`);
      }
      const a = hits[0].attributes ?? {};
      return { id: hits[0].id, name: a.name ?? "", isInternal: a.isInternalGroup === true, allBuilds: a.hasAccessToAllBuilds === true };
    },

    async groupId(appId, name) {
      return (await this.groupInfo(appId, { name })).id;
    },

    async waitValid(appId, version, build, timeoutMin, intervalS) {
      const deadline = now() + timeoutMin * 60 * 1000;
      for (;;) {
        const page = await request("GET", "/v1/builds", {
          query: { "filter[app]": appId, "filter[version]": String(build), "filter[preReleaseVersion.version]": version },
        });
        const found = page?.data?.[0];
        const state = found?.attributes?.processingState ?? "PROCESSING"; // not yet listed counts as PROCESSING
        if (state === "VALID") return found.id;
        if (state === "INVALID" || state === "FAILED") throw new AscError(8, `build ${build} processing ended as ${state}`);
        if (now() >= deadline) throw new AscError(8, `build ${build} was not VALID within ${timeoutMin} minutes (last state ${state})`);
        await sleep(intervalS * 1000);
      }
    },

    async setWhatsNew(buildId, locale, text) {
      try {
        const locs = await getAll(`/v1/builds/${encodeURIComponent(buildId)}/betaBuildLocalizations`);
        const existing = locs.find((l) => l.attributes?.locale === locale);
        if (existing) {
          await request("PATCH", `/v1/betaBuildLocalizations/${encodeURIComponent(existing.id)}`, {
            body: { data: { type: "betaBuildLocalizations", id: existing.id, attributes: { whatsNew: text } } },
          });
        } else {
          await request("POST", "/v1/betaBuildLocalizations", {
            body: { data: { type: "betaBuildLocalizations", attributes: { locale, whatsNew: text }, relationships: { build: rel("builds", buildId) } } },
          });
        }
      } catch (err) {
        if (err instanceof AscError) throw new AscError(9, `setting What to Test failed: ${err.message}`);
        throw err;
      }
    },

    // Returns "skipped-all-builds", "skipped-external" or "added".
    async addToGroup(buildId, groupId, { submitExternal = false } = {}) {
      try {
        const g = await request("GET", `/v1/betaGroups/${encodeURIComponent(groupId)}`);
        const a = g?.data?.attributes ?? {};
        if (a.isInternalGroup === true && a.hasAccessToAllBuilds === true) {
          process.stderr.write(`group ${groupId}: skipped: gets all builds\n`);
          return "skipped-all-builds";
        }
        if (a.isInternalGroup !== true && !submitExternal) {
          process.stderr.write(`group ${groupId}: skipped: external group; set submitExternal to add it (this submits the build to beta app review)\n`);
          return "skipped-external";
        }
        await request("POST", `/v1/betaGroups/${encodeURIComponent(groupId)}/relationships/builds`, { body: { data: [{ type: "builds", id: buildId }] } });
        return "added";
      } catch (err) {
        if (err instanceof AscError) throw new AscError(9, `adding the build to the beta group failed: ${err.message}`);
        throw err;
      }
    },
  };
}

// ---- CLI ----
const COMMANDS = {
  "app-id": { flags: ["bundle-id"] },
  "next-build": { flags: ["app-id", "version"] },
  "group-id": { flags: ["app-id", "name"] },
  "group-info": { flags: ["app-id"], optional: ["id", "name", "internal"] },
  "wait-valid": { flags: ["app-id", "version", "build", "timeout-min", "interval-s"] },
  "set-whats-new": { flags: ["build-id", "locale", "notes-file"] },
  "add-to-group": { flags: ["build-id", "group-id"], bool: ["submit-external"] },
};

export const HELP = `usage: node asc.mjs <command> [flags]   (credentials: ASC_KEY_ID, ASC_ISSUER_ID, ASC_KEY_PATH in the environment)
Commands (each prints one value on stdout; exit 2 usage, 4 credentials, 6 ASC API error, 8 processing, 9 post-processing):
  app-id --bundle-id ID
  next-build --app-id A --version V
  group-id --app-id A --name N
  group-info --app-id A (--id ID | --name N [--internal true|false])
        prints "<id> TAB internal|external TAB all|selected TAB <group name>". Exactly one of --id / --name;
        --internal only with --name. Several matches exit 6 and list every id.
  wait-valid --app-id A --version V --build N --timeout-min M --interval-s S
  set-whats-new --build-id B --locale L --notes-file F
  add-to-group --build-id B --group-id G [--submit-external]
  --help`;

export function parseArgs(argv) {
  const [cmd, ...rest] = argv;
  const spec = Object.hasOwn(COMMANDS, cmd) ? COMMANDS[cmd] : null;
  if (!spec) throw new AscError(2, `unknown or missing command; expected one of: ${Object.keys(COMMANDS).join(", ")}`);
  const flags = {};
  for (let i = 0; i < rest.length; i += 2) {
    const name = rest[i].startsWith("--") ? rest[i].slice(2) : null;
    if (name && spec.bool?.includes(name)) {
      flags[name] = true;
      i -= 1; // boolean flag: no value
      continue;
    }
    if (!name || !(spec.flags.includes(name) || spec.optional?.includes(name))) throw new AscError(2, `unknown flag for ${cmd}: ${rest[i]}`);
    if (i + 1 >= rest.length) throw new AscError(2, `flag --${name} needs a value`);
    flags[name] = rest[i + 1];
  }
  const missing = spec.flags.filter((f) => !(f in flags));
  if (missing.length) throw new AscError(2, `${cmd} needs: ${missing.map((m) => "--" + m).join(" ")}`);
  if (cmd === "group-info") {
    if (("id" in flags) === ("name" in flags)) throw new AscError(2, "group-info needs exactly one of --id or --name");
    if ("internal" in flags && !("name" in flags)) throw new AscError(2, "--internal can only be used with --name");
    if ("internal" in flags && !["true", "false"].includes(flags["internal"])) throw new AscError(2, "--internal must be true or false");
  }
  return { cmd, flags };
}

const num = (name, v) => {
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0) throw new AscError(2, `--${name} must be a non-negative number`);
  return n;
};

export async function runCli(argv, env, io = {}) {
  if (["--help", "-h"].includes(argv[0])) return HELP;
  const { cmd, flags } = parseArgs(argv);
  const baseUrl = resolveBaseUrl(env.HIVEMILL_ASC_API_BASE);
  const missing = ["ASC_KEY_ID", "ASC_ISSUER_ID", "ASC_KEY_PATH"].filter((k) => !env[k]);
  if (missing.length) throw new AscError(4, `missing environment variable(s): ${missing.join(", ")}`);
  let notes;
  if (cmd === "set-whats-new") {
    try {
      notes = fs.readFileSync(flags["notes-file"], "utf8");
    } catch {
      throw new AscError(2, "cannot read the --notes-file");
    }
  }
  const keyPem = loadKey(env.ASC_KEY_PATH);
  const client = createClient({ keyId: env.ASC_KEY_ID, issuerId: env.ASC_ISSUER_ID, keyPem, baseUrl, now: io.now, sleep: io.sleep, fetch: io.fetch, timeoutMs: io.timeoutMs });
  switch (cmd) {
    case "app-id":
      return String(await client.appId(flags["bundle-id"]));
    case "next-build":
      return String(await client.nextBuildNumber(flags["app-id"], flags.version));
    case "group-id":
      return String(await client.groupId(flags["app-id"], flags.name));
    case "group-info": {
      const sel = flags.id !== undefined ? { id: flags.id } : { name: flags.name, ...(flags["internal"] !== undefined ? { internal: flags["internal"] === "true" } : {}) };
      const g = await client.groupInfo(flags["app-id"], sel);
      return `${g.id}\t${g.isInternal ? "internal" : "external"}\t${g.allBuilds ? "all" : "selected"}\t${g.name}`;
    }
    case "wait-valid":
      return String(await client.waitValid(flags["app-id"], flags.version, flags.build, num("timeout-min", flags["timeout-min"]), num("interval-s", flags["interval-s"])));
    case "set-whats-new":
      await client.setWhatsNew(flags["build-id"], flags.locale, notes);
      return null;
    default:
      return await client.addToGroup(flags["build-id"], flags["group-id"], { submitExternal: flags["submit-external"] === true });
  }
}

function isMain() {
  try {
    return process.argv[1] && fs.realpathSync(process.argv[1]) === fs.realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isMain()) {
  try {
    const out = await runCli(process.argv.slice(2), process.env);
    if (out !== null) process.stdout.write(out + "\n");
  } catch (err) {
    if (err instanceof AscError) {
      process.stderr.write(`asc: ${err.message}\n`);
      process.exit(err.code);
    }
    process.stderr.write("asc: unexpected internal error\n");
    process.exit(1);
  }
}
