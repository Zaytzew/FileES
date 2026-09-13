package memoryguard

import (
	"fmt"
	"syscall"
	"unsafe"
)

var memoryKernel = syscall.NewLazyDLL("kernel32.dll")
var memoryStatus = memoryKernel.NewProc("GlobalMemoryStatusEx")
var currentProcess = memoryKernel.NewProc("GetCurrentProcess")
var processMemory = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func ReadSample() (Sample, error) {
	var mem struct {
		Length, Load                                                                         uint32
		Total, Available, TotalPage, AvailablePage, TotalVirtual, AvailableVirtual, Extended uint64
	}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, err := memoryStatus.Call(uintptr(unsafe.Pointer(&mem))); ok == 0 {
		return Sample{}, fmt.Errorf("memory status: %w", err)
	}
	var counters struct {
		Size, Faults                                                                                    uint32
		PeakWorking, Working, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile, Private uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	process, _, _ := currentProcess.Call()
	if ok, _, err := processMemory.Call(process, uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size)); ok == 0 {
		return Sample{}, fmt.Errorf("process memory: %w", err)
	}
	return addRuntime(Sample{PrivateBytes: uint64(counters.Private), TotalBytes: mem.Total, AvailableBytes: mem.Available}), nil
}
