package agent

import (
	"context"
	"math"
	"testing"
)

// Review Focus #5: ParseNvidiaSMIOutput parses single/multi-GPU CSV and missing nvidia-smi degrades gracefully.
func TestParseNvidiaSMIOutput(t *testing.T) {
	gpu, mem, temp, ok := ParseNvidiaSMIOutput("72, 4096, 16384, 65\n")
	if !ok {
		t.Fatal("expected single-GPU CSV parse ok=true")
	}
	if math.Abs(float64(gpu)-72.0) > 0.1 || math.Abs(float64(mem)-25.0) > 0.1 || math.Abs(float64(temp)-65.0) > 0.1 {
		t.Fatalf("unexpected single-GPU metrics: gpu=%v mem=%v temp=%v", gpu, mem, temp)
	}

	// Multi-GPU averaging
	gpu2, mem2, temp2, ok2 := ParseNvidiaSMIOutput("50, 2000, 10000, 60\n90, 6000, 10000, 70\n")
	if !ok2 {
		t.Fatal("expected multi-GPU CSV parse ok=true")
	}
	if math.Abs(float64(gpu2)-70.0) > 0.1 || math.Abs(float64(mem2)-40.0) > 0.1 || math.Abs(float64(temp2)-65.0) > 0.1 {
		t.Fatalf("unexpected multi-GPU averages: gpu=%v mem=%v temp=%v", gpu2, mem2, temp2)
	}

	// Malformed or empty output
	if _, _, _, bad := ParseNvidiaSMIOutput("NVIDIA-SMI has failed"); bad {
		t.Fatal("expected malformed output to return ok=false")
	}
}

func TestCollectHostMetricsGracefulDegradationWithoutGPU(t *testing.T) {
	m := collectHostMetricsWithBinary(context.Background(), "nonexistent-nvidia-smi-binary-xyz")
	if m == nil {
		t.Fatal("expected non-nil HostMetrics")
	}
	if m.GetHasGpu() {
		t.Fatalf("expected has_gpu=false when nvidia-smi is absent, got %+v", m)
	}
	if m.GetCpuPercent() < 0 || m.GetCpuPercent() > 100 {
		t.Fatalf("cpu_percent out of range: %v", m.GetCpuPercent())
	}
	if m.GetMemoryPercent() <= 0 || m.GetMemoryPercent() > 100 {
		t.Fatalf("expected valid host memory_percent > 0 and <= 100, got %v", m.GetMemoryPercent())
	}
}
