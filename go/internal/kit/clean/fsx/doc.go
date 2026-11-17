// Package fsx is the safety core of hm-clean: every delete goes through it.
// It enforces the root boundary (os.Root, openat-style), never follows
// symlinks, never crosses filesystems, refuses to run as root, caps the bytes
// removed per run and per rule, re-checks item identity before deleting, and
// writes JSON plan/apply logs atomically. Stdlib only; builds on linux and
// darwin.
package fsx
