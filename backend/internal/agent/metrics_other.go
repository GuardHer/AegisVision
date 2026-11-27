//go:build !linux && !windows

package agent

import "context"

func collectCPUMemPercent(_ context.Context) (cpuPct, memPct float32) {
	return 0, 0
}
