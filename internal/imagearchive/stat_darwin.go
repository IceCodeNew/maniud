//go:build darwin

package imagearchive

import "syscall"

func statDevice(value *syscall.Stat_t) uint64 {
	return uint64(value.Dev)
}

func statInode(value *syscall.Stat_t) uint64 {
	return value.Ino
}

func statChangeTime(value *syscall.Stat_t) int64 {
	return value.Ctimespec.Sec*1_000_000_000 + value.Ctimespec.Nsec
}
