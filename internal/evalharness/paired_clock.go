package evalharness

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type PairClock struct {
	BootID      string `json:"boot_id"`
	Nanoseconds int64  `json:"nanoseconds"`
}

func pairClockNow() (*PairClock, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	var stamp unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &stamp); err != nil {
		return nil, err
	}
	return &PairClock{BootID: strings.TrimSpace(string(boot)), Nanoseconds: stamp.Nano()}, nil
}

func pairClockElapsed(start, end *PairClock) (*float64, error) {
	if start == nil && end == nil {
		return nil, nil
	}
	if start == nil || end == nil || start.BootID == "" || start.BootID != end.BootID || start.Nanoseconds < 0 || end.Nanoseconds < start.Nanoseconds {
		return nil, fmt.Errorf("paired monotonic clock identity or interval is invalid")
	}
	seconds := float64(end.Nanoseconds-start.Nanoseconds) / 1e9
	return &seconds, nil
}
