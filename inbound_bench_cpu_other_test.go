//go:build !unix

package acp

import "time"

// processCPU returns 0: the benchmarks report CPU time only on Unix.
func processCPU() time.Duration { return 0 }
