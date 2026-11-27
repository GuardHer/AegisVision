package agent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
)

const nvidiaSMITimeout = 1 * time.Second

// CollectHostMetrics collects zero-CGO host CPU/memory usage plus optional NVIDIA GPU telemetry.
// On hosts without nvidia-smi or GPUs, HasGpu is explicitly false while CPU/memory remain populated.
func CollectHostMetrics(ctx context.Context) *agentv1.HostMetrics {
	return collectHostMetricsWithBinary(ctx, "nvidia-smi")
}

func collectHostMetricsWithBinary(ctx context.Context, nvidiaBinary string) *agentv1.HostMetrics {
	cpuPct, memPct := collectCPUMemPercent(ctx)
	m := &agentv1.HostMetrics{
		CpuPercent:    clampPercent(cpuPct),
		MemoryPercent: clampPercent(memPct),
		HasGpu:        false,
	}
	if gpuPct, gpuMemPct, tempC, ok := probeNvidiaSMI(ctx, nvidiaBinary); ok {
		m.HasGpu = true
		m.GpuPercent = clampPercent(gpuPct)
		m.GpuMemoryPercent = clampPercent(gpuMemPct)
		m.GpuTempC = tempC
	}
	return m
}

// ParseNvidiaSMIOutput parses CSV output from:
// `nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits`.
// Multi-GPU hosts are averaged across all valid GPU rows.
func ParseNvidiaSMIOutput(raw string) (gpuPct, memPct, tempC float32, ok bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var sumGPU, sumMem, sumTemp float64
	var count int

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		uGPU, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		memUsed, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		memTotal, err3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		temp, err4 := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || memTotal <= 0 {
			continue
		}
		sumGPU += uGPU
		sumMem += (memUsed / memTotal) * 100.0
		sumTemp += temp
		count++
	}
	if count == 0 {
		return 0, 0, 0, false
	}
	n := float64(count)
	return clampPercent(float32(sumGPU / n)), clampPercent(float32(sumMem / n)), float32(sumTemp / n), true
}

func probeNvidiaSMI(ctx context.Context, binary string) (gpuPct, memPct, tempC float32, ok bool) {
	if strings.TrimSpace(binary) == "" {
		return 0, 0, 0, false
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return 0, 0, 0, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, nvidiaSMITimeout)
	defer cancel()

	// #nosec G204 -- fixed nvidia-smi read-only query flags
	cmd := exec.CommandContext(probeCtx, path,
		"--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, false
	}
	return ParseNvidiaSMIOutput(string(out))
}

func clampPercent(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
