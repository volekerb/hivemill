// A stand-in for the app's "herdr" module, only for the node tests. Tests fill S, the mod reads it through the same
// functions the app provides. Nothing here touches the network, a real app or the real home.
export const S = {};

export function reset() {
  for (const k of Object.keys(S)) delete S[k];
  Object.assign(S, {
    shared: {}, files: {}, agents: [], terminals: [], spaces: [], machines: [], screens: {},
    http: {}, // "<machine|local> <url>" -> JSON body, or an Error / missing for an unreachable team
    hcCalls: [], execCalls: [],
  });
}
reset();

export const home = () => "/fixture";
export const shared = (k) => S.shared[k];
export const setShared = (k, v) => {
  S.shared[k] = v;
};
export const readTextFile = async (p) => S.files[p] ?? null;
export const agents = () => S.agents;
export const terminals = () => S.terminals;
export const spaces = () => S.spaces;
export const machines = () => S.machines;

export async function exec(cmd, args, opts = {}) {
  const on = opts.on ?? null;
  S.execCalls.push({ cmd, args, on });
  if (cmd === "curl") {
    const body = S.http[`${on ?? "local"} ${args[args.length - 1]}`];
    if (body === undefined || body instanceof Error) return { code: 7, stdout: "", stderr: "curl: (7) Failed to connect" };
    return { code: 0, stdout: JSON.stringify(body), stderr: "" };
  }
  if (cmd === "sh") return { code: 0, stdout: "os=Linux\narch=x86_64\ncpus=4\nmem_total_mb=8000\nmem_avail_mb=4000\nload=0.5,0.5,0.5\nuptime_s=3600\ndisk_total_gb=100\ndisk_free_gb=60\n", stderr: "" };
  return { code: 1, stdout: "", stderr: `stub: ${cmd} not handled` };
}

export async function hc(args) {
  S.hcCalls.push(args);
  if (args[0] === "read") {
    if (!(args[1] in S.screens)) throw new Error(`stub: no screen for ${args[1]}`);
    return { code: 0, stdout: S.screens[args[1]], stderr: "" };
  }
  return { code: 1, stdout: "", stderr: "stub: unhandled" };
}
