# Go toolchain (G-016)

The factory's Go code lives in `go/` (module `factory/go`, stdlib only: no `require` lines, no `go.sum`). The toolchain is installed at user level on your-box; no root, no apt.

Pinned version: **go1.27.3** (`go/go.mod`: `go 1.27`, `toolchain go1.27.3`).
sha256 of `go1.27.3.linux-amd64.tar.gz`: `65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40` (from https://go.dev/dl/?mode=json).

Coders do not install toolchains. If `go` is missing from PATH, report to your lead; the install is a lead/director-arranged step.

## Install into ~/.local/go

```
cd "$(mktemp -d)"
curl -fsSLO https://go.dev/dl/go1.27.3.linux-amd64.tar.gz
echo "65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40  go1.27.3.linux-amd64.tar.gz" | sha256sum -c
rm -rf ~/.local/go && mkdir -p ~/.local
tar -C ~/.local -xzf go1.27.3.linux-amd64.tar.gz
```

`sha256sum -c` must print `OK`; stop if it does not.

## Environment

```
export PATH=~/.local/go/bin:$PATH
export GOTOOLCHAIN=local          # never download another toolchain
export GOFLAGS=-mod=readonly
export GOPATH=~/.cache/go/path
export GOCACHE=~/.cache/go/build
```

Check: `go version` prints `go1.27.3`.

## Upgrade

1. Pick the new release from https://go.dev/dl/ and read its sha256 for `linux-amd64.tar.gz` from https://go.dev/dl/?mode=json.
2. Bump `go` and `toolchain` in `go/go.mod`, and the version and sha256 in this file.
3. Re-run the install above with the new version and `sha256sum -c`.
4. Open a PR. CI (`actions/setup-go` with `go-version-file: go/go.mod`, `GOTOOLCHAIN=local`) picks the new version up from `go.mod`.
