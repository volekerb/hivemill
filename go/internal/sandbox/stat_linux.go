//go:build linux

package sandbox

import "syscall"

func mtimeNano(st *syscall.Stat_t) int64 { return st.Mtim.Nano() }
