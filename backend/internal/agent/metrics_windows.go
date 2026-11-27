//go:build windows

package agent

import (
	"context"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32DLL              = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemTimes       = kernel32DLL.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32DLL.NewProc("GlobalMemoryStatusEx")

	winCPUMu    sync.Mutex
	winPrevIdle uint64
	winPrevTot  uint64
	winHasPrev  bool
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func filetimeToUint64(ft syscall.Filetime) uint64 {
	return (uint64(ft.HighDateTime) << 32) | uint64(ft.LowDateTime)
}

func sampleWinSystemTimes() (idle, total uint64, ok bool) {
	var idleFT, kernelFT, userFT syscall.Filetime
	// #nosec G103 -- standard Win32 GetSystemTimes syscall pointer passing (zero CGO)
	ret, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleFT)),
		uintptr(unsafe.Pointer(&kernelFT)),
		uintptr(unsafe.Pointer(&userFT)),
	)
	if ret == 0 {
		return 0, 0, false
	}
	idle = filetimeToUint64(idleFT)
	// kernelFT already includes idleFT on Windows
	total = filetimeToUint64(kernelFT) + filetimeToUint64(userFT)
	return idle, total, total > 0
}

func collectCPUMemPercent(ctx context.Context) (cpuPct, memPct float32) {
	cpuPct = readWinCPUPercent(ctx)
	memPct = readWinMemPercent()
	return cpuPct, memPct
}

func readWinCPUPercent(ctx context.Context) float32 {
	idle, total, ok := sampleWinSystemTimes()
	if !ok {
		return 0
	}

	winCPUMu.Lock()
	defer winCPUMu.Unlock()

	if !winHasPrev {
		winPrevIdle = idle
		winPrevTot = total
		winHasPrev = true
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(100 * time.Millisecond):
		}
		idle2, total2, ok2 := sampleWinSystemTimes()
		if !ok2 || total2 <= total {
			return 0
		}
		dIdle := idle2 - idle
		dTot := total2 - total
		winPrevIdle = idle2
		winPrevTot = total2
		if dIdle > dTot {
			return 0
		}
		return float32(float64(dTot-dIdle) / float64(dTot) * 100.0)
	}

	if total <= winPrevTot {
		return 0
	}
	dIdle := idle - winPrevIdle
	dTot := total - winPrevTot
	winPrevIdle = idle
	winPrevTot = total
	if dIdle > dTot {
		return 0
	}
	return float32(float64(dTot-dIdle) / float64(dTot) * 100.0)
}

func readWinMemPercent() float32 {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	// #nosec G103 -- standard Win32 GlobalMemoryStatusEx syscall pointer passing (zero CGO)
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if ret == 0 || st.TotalPhys == 0 {
		return 0
	}
	if st.AvailPhys > st.TotalPhys {
		return float32(st.MemoryLoad)
	}
	return float32(float64(st.TotalPhys-st.AvailPhys) / float64(st.TotalPhys) * 100.0)
}
