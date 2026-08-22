//go:build linux

package imagearchive

import "syscall"

func statDevice(value *syscall.Stat_t) uint64 {
	return value.Dev
}

func statInode(value *syscall.Stat_t) uint64 {
	return value.Ino
}

func statChangeTime(value *syscall.Stat_t) int64 {
	return value.Ctim.Sec*1_000_000_000 + value.Ctim.Nsec
}
