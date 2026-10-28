"""Egress pieces of the eval sandbox: the allow-listing CONNECT proxy (runs OUTSIDE the namespace, listens on a
unix socket) and the loopback forwarder (runs inside, 127.0.0.1:3128 -> that socket). Stdlib only.

Run as a script by scout/nsbox.py: `egress.py serve <json>` (the proxy, detached), `egress.py forward <sock>`
"""
import json
import os
import selectors
import socket
import sys
import threading
import time

ALLOW = "api.anthropic.com:443"
MAX_LIFE = 20 * 60
DENIED = b"HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
OK = b"HTTP/1.1 200 Connection Established\r\n\r\n"


def default_resolver(host, port):
    """host:port -> something socket can connect to. Only ever called for allow-listed targets."""
    return socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)[0][4][:2]


def pid_alive(pid):
    """False for a missing pid and for a zombie (kill(pid, 0) alone says yes to a zombie)."""
    try:
        with open(f"/proc/{int(pid)}/stat") as f:
            stat = f.read()
    except (OSError, ValueError):
        return False
    return stat[stat.rindex(")") + 2:][:1] not in ("Z", "X")


def _connect(addr):
    fam = socket.AF_UNIX if isinstance(addr, str) else socket.AF_INET
    s = socket.socket(fam, socket.SOCK_STREAM)
    s.settimeout(10)
    s.connect(addr)
    s.settimeout(None)
    return s


def relay(a, b):
    """Copy bytes both ways until either side closes."""
    sel = selectors.DefaultSelector()
    sel.register(a, selectors.EVENT_READ, b)
    sel.register(b, selectors.EVENT_READ, a)
    try:
        while True:
            for key, _ in sel.select():
                try:
                    data = key.fileobj.recv(65536)
                    if not data:
                        return
                    key.data.sendall(data)
                except OSError:
                    return
    finally:
        sel.close()
        a.close()
        b.close()


def _read_head(conn):
    conn.settimeout(10)
    buf = b""
    while b"\r\n\r\n" not in buf and len(buf) < 8192:
        chunk = conn.recv(4096)
        if not chunk:
            break
        buf += chunk
    conn.settimeout(None)
    head, _, rest = buf.partition(b"\r\n\r\n")
    return head, rest


def allowed_target(request_line, allow):
    """host:port of an allowed CONNECT request line, else None. Exact match, host case-insensitive."""
    parts = request_line.split()
    if len(parts) != 3 or parts[0] != "CONNECT" or not parts[2].startswith("HTTP/"):
        return None
    host, _, port = parts[1].rpartition(":")
    want_host, _, want_port = allow.rpartition(":")
    if host.lower() == want_host.lower() and port == want_port:
        return host, int(port)
    return None


def handle(conn, allow, resolver):
    try:
        head, rest = _read_head(conn)
        target = allowed_target(head.split(b"\r\n")[0].decode("latin-1"), allow)
        if target is None:  # denied: no DNS, no upstream connection
            conn.sendall(DENIED)
            conn.close()
            return
        try:
            up = _connect(resolver(*target))
        except OSError:
            conn.sendall(b"HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
            conn.close()
            return
        conn.sendall(OK)
        if rest:
            up.sendall(rest)
        relay(conn, up)
    except OSError:
        conn.close()


def serve(sock_path, allow=ALLOW, watch_pid=None, max_life=MAX_LIFE, resolver=default_resolver, ready=None):
    """Accept loop. Returns when the watched pid is gone or max_life elapsed. `ready` is set once listening."""
    if os.path.exists(sock_path):
        os.unlink(sock_path)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(sock_path)
    os.chmod(sock_path, 0o600)
    srv.listen(16)
    srv.settimeout(0.3)
    if ready is not None:
        ready.set()
    deadline = time.monotonic() + max_life
    try:
        while time.monotonic() < deadline and (watch_pid is None or pid_alive(watch_pid)):
            try:
                conn, _ = srv.accept()
            except socket.timeout:
                continue
            threading.Thread(target=handle, args=(conn, allow, resolver), daemon=True).start()
    finally:
        srv.close()
        try:
            os.unlink(sock_path)
        except OSError:
            pass


def forward(sock_path, host="127.0.0.1", port=3128):
    """Inside the namespace: every TCP connection on host:port goes to the proxy's unix socket. Never returns."""
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((host, port))
    srv.listen(16)

    def one(c):
        try:
            relay(c, _connect(sock_path))
        except OSError:
            c.close()

    while True:
        c, _ = srv.accept()
        threading.Thread(target=one, args=(c,), daemon=True).start()


def upstream_resolver(table):
    """Test seam: host:port -> address from a table, else the real DNS answer."""
    def resolve(host, port):
        hit = table.get(f"{host.lower()}:{port}")
        return (hit[0], hit[1]) if isinstance(hit, list) else hit or default_resolver(host, port)
    return resolve


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "serve":
        cfg = json.loads(sys.argv[2])
        serve(cfg["sock"], cfg.get("allow", ALLOW), cfg.get("watch_pid"), cfg.get("max_life", MAX_LIFE),
              upstream_resolver(cfg.get("upstream", {})))
    elif len(sys.argv) == 3 and sys.argv[1] == "forward":
        forward(sys.argv[2])
    else:
        sys.exit("usage: egress.py serve <json: sock, allow, watch_pid, max_life, upstream> | forward <sock>")
