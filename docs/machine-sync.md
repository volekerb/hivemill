# machine-sync: copy secret files to a worker machine

`worker-machine-sync` is a manual owner tool (G-057 SC7). It copies an allow-list of files, such as `.env` files, from this
machine to a worker machine such as `wbox2`. Nothing in the factory calls it, and it needs no factory config.

The tool never prints, logs or hashes file content. The remote file is created with mode 600 from its first byte
(`umask 077`, written to a temp file, then renamed). Missing parent directories are created with mode 700. After each copy
the tool runs a remote `stat -c %a` and exits 1 with `mode check failed` unless the mode is 600.

## 1. Build the list

Create a JSON file outside the repo (never commit it). It is an array of `{src, dst}` entries with absolute paths:

```json
[
  {"src": "/home/<you>/projects/app/.env", "dst": "/home/<you>/projects/app/.env"}
]
```

Rules, enforced before any copy (exit 2, the message names the path, never content):

- `src` must be a regular file under your local home. Symlinks, directories and paths outside home are refused.
  The file is opened without following symlinks and must still be the same file (device and inode) that was checked,
  so swapping it for a symlink or another file between the check and the copy is refused.
- Size caps: each `src` at most 1 MiB, all files together at most 16 MiB, the list file at most 1 MiB.
- `dst` must be a clean absolute path under the remote home (the tool asks the remote for its `$HOME`).
- `--machine` must not contain "factory" (case-insensitive).

## 2. Dry run

```
worker-machine-sync --machine wbox2 --list ~/machine-sync-list.json --dry-run
```

Prints `would copy <src> -> <dst>` per entry and copies nothing. It makes one read-only ssh call to learn the remote home.

## 3. Real copy

```
worker-machine-sync --machine wbox2 --list ~/machine-sync-list.json
```

Prints `copied <dst> mode 600` per file. Any ssh error text is passed through, but every line that contains bytes of the
file being copied is replaced by `[redacted: line contained file content]`.

Every ssh call uses `BatchMode=yes`, `ConnectTimeout=10` and `ServerAliveInterval=5`, and is killed after 60 seconds; the
whole run is killed after 10 minutes. So an unreachable machine fails with `timed out` (exit 1) instead of hanging. A `dst` that is an existing directory fails
with `dst is a directory`.

Exit codes: 0 ok, 1 ssh/copy/mode failure, 2 refused or bad usage.

Installation of the machines themselves is described in `docs/multi-machine.md` (owned by another task).
