//go:build windows

package storage

import "golang.org/x/sys/windows"

func Available(path string) (int64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &available, nil, nil); err != nil {
		return 0, err
	}
	return product(available, 1), nil
}
