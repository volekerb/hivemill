// Package cdp is the shared Chrome DevTools Protocol client and the pinned chrome-headless-shell
// (download, size+sha256 verification, unzip, launch). It was moved out of go/cmd/film-render so
// other tools can drive a real browser. The pin must equal the one in ci/screenshots.py
// (TestBrowserPin compares them).
package cdp
