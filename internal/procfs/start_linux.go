//go:build linux

// Package procfs reads the small Linux process facts used to pin local daemon peers.
package procfs

import (
	"bytes"
	"io"
	"os"
	"strconv"
)

const (
	maximumProcessStatBytes = 4096
	processStartTimeIndex   = 19
)

// StartTime returns the process start time from /proc/PID/stat. The value is
// measured in clock ticks since boot and distinguishes reused process IDs.
func StartTime(process int32) (uint64, bool) {
	if process <= 0 {
		return 0, false
	}

	file, err := os.Open("/proc/" + strconv.FormatInt(int64(process), 10) + "/stat")
	if err != nil {
		return 0, false
	}

	return readStartTime(file)
}

func readStartTime(file io.ReadCloser) (uint64, bool) {
	value, readErr := io.ReadAll(io.LimitReader(file, maximumProcessStatBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(value) > maximumProcessStatBytes {
		return 0, false
	}

	return decodeStartTime(value)
}

func decodeStartTime(value []byte) (uint64, bool) {
	closing := bytes.LastIndexByte(value, ')')
	if closing < 0 || closing+2 >= len(value) {
		return 0, false
	}
	fields := bytes.Fields(value[closing+2:])
	if len(fields) <= processStartTimeIndex {
		return 0, false
	}
	generation, err := strconv.ParseUint(string(fields[processStartTimeIndex]), 10, 64)
	if err != nil || generation == 0 {
		return 0, false
	}

	return generation, true
}
