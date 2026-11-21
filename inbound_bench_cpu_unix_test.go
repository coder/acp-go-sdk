//go:build unix

package acp

import (
	"syscall"
	"time"
)

// processCPU returns the CPU time that the process has used so far, in user
// and system mode. Unlike wall time, it does not grow while other processes
// hold the CPU, so it is the steadier measure on a shared machine.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
