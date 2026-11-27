//go:build linux

package agent

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	linuxCPUMu    sync.Mutex
	linuxPrevIdle uint64
	linuxPrevTot  uint64
	linuxHasPrev  bool
)

func collectCPUMemPercent(ctx context.Context) (cpuPct, memPct float32) {
	cpuPct = readLinuxCPUPercent(ctx)
	memPct = readLinuxMemPercent()
	return cpuPct, memPct
}

func readLinuxCPUPercent(ctx context.Context) float32 {
	idle, total, ok := sampleLinuxProcStat()
	if !ok {
		return 0
	}

	linuxCPUMu.Lock()
	defer linuxCPUMu.Unlock()

	if !linuxHasPrev {
		linuxPrevIdle = idle
		linuxPrevTot = total
		linuxHasPrev = true
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(100 * time.Millisecond):
		}
		idle2, total2, ok2 := sampleLinuxProcStat()
		if !ok2 || total2 <= total {
			return 0
		}
		dIdle := idle2 - idle
		dTot := total2 - total
		linuxPrevIdle = idle2
		linuxPrevTot = total2
		return float32(float64(dTot-dIdle) / float64(dTot) * 100.0)
	}

	if total <= linuxPrevTot {
		return 0
	}
	dIdle := idle - linuxPrevIdle
	dTot := total - linuxPrevTot
	linuxPrevIdle = idle
	linuxPrevTot = total
	if dIdle > dTot {
		return 0
	}
	return float32(float64(dTot-dIdle) / float64(dTot) * 100.0)
}

func sampleLinuxProcStat() (idle, total uint64, ok bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	firstLine, _, _ := strings.Cut(string(raw), "\n")
	fields := strings.Fields(firstLine)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i := 1; i < len(fields); i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += v
		if i == 4 || i == 5 { // idle + iowait
			idle += v
		}
	}
	return idle, total, total > 0
}

func readLinuxMemPercent() float32 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var totalKB, availKB uint64
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				totalKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				availKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	if totalKB == 0 || availKB > totalKB {
		return 0
	}
	return float32(float64(totalKB-availKB) / float64(totalKB) * 100.0)
}
