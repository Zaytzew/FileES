//go:build windows

package runtime

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(h)
		return true
	}
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}
