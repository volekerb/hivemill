// In-process fake of the ASC API on 127.0.0.1 (port 0). Records every request and serves scripted responses.
// mock.on(method, path, [{status, body}, ...]): responses are served in order; the last one repeats.
import http from "node:http";

export async function startMock() {
  const routes = new Map();
  const requests = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const url = new URL(req.url, "http://127.0.0.1");
      const raw = Buffer.concat(chunks).toString();
      requests.push({
        method: req.method,
        path: url.pathname,
        query: Object.fromEntries(url.searchParams),
        authorization: req.headers.authorization,
        body: raw ? JSON.parse(raw) : null,
      });
      const queue = routes.get(`${req.method} ${url.pathname}`);
      // Unscripted GET /v1/betaGroups/<id> serves an internal group with selected builds.
      const groupDefault = req.method === "GET" && /^\/v1\/betaGroups\/[^/]+$/.test(url.pathname) ? { status: 200, body: groupBody(url.pathname.split("/").pop(), true, false) } : null;
      const next = queue ? (queue.length > 1 ? queue.shift() : queue[0]) : groupDefault ?? { status: 404, body: { errors: [{ title: "unscripted" }] } };
      if (next.hang) return; // never answer: the client must time out
      const typeof_ = typeof next.body === "function" ? next.body(url) : next.body;
      res.writeHead(next.status, { "Content-Type": "application/json" });
      res.end(next.status === 204 ? "" : JSON.stringify(typeof_ ?? {}));
    });
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const port = server.address().port;
  return {
    port,
    baseUrl: `http://127.0.0.1:${port}`,
    requests,
    on(method, path, responses) {
      routes.set(`${method} ${path}`, [...responses]);
    },
    close: () => new Promise((r) => { server.closeAllConnections?.(); server.close(r); }),
  };
}

export function fakeClock(startMs = 1_700_000_000_000) {
  const clock = { t: startMs, sleeps: [], now: () => clock.t, sleep: async (ms) => { clock.sleeps.push(ms / 1000); clock.t += ms; } };
  return clock;
}

// Body of GET /v1/betaGroups/<id>.
export const groupBody = (id, isInternalGroup, hasAccessToAllBuilds) => ({ data: { id, type: "betaGroups", attributes: { isInternalGroup, hasAccessToAllBuilds } } });
