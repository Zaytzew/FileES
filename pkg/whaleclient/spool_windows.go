//go:build windows

package whaleclient

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const ioctlVolumeGetVolumeDiskExtents = uint32(0x00560000)

func (m *Manager) systemSpoolCandidates(sourcePath string) ([]spoolCandidate, error) {
	rootBuffer := make([]uint16, 512)
	n, err := windows.GetLogicalDriveStrings(uint32(len(rootBuffer)), &rootBuffer[0])
	if err != nil {
		return nil, err
	}
	if n >= uint32(len(rootBuffer)) {
		return nil, errors.New("logical drive list exceeds Whale buffer")
	}
	_, defaultVolume, _, defaultErr := systemPathIdentity(m.Root)
	var candidates []spoolCandidate
	for _, root := range splitUTF16List(rootBuffer[:n]) {
		pointer, pointerErr := windows.UTF16PtrFromString(root)
		if pointerErr != nil || windows.GetDriveType(pointer) != windows.DRIVE_FIXED {
			continue
		}
		volumeRoot, volumeID, deviceID, identityErr := systemPathIdentity(root)
		if identityErr != nil {
			continue
		}
		available, availableErr := filesystemAvailable(volumeRoot)
		if availableErr != nil {
			continue
		}
		spoolRoot := filepath.Join(volumeRoot, ".filees-whales")
		if defaultErr == nil && volumeID == defaultVolume {
			spoolRoot = m.Root
		}
		candidates = append(candidates, spoolCandidate{Root: spoolRoot, VolumeID: volumeID, DeviceID: deviceID, Available: available})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].VolumeID < candidates[j].VolumeID })
	return candidates, nil
}

func systemPathIdentity(path string) (root, volumeID, deviceID string, err error) {
	pathPointer, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return "", "", "", err
	}
	rootBuffer := make([]uint16, 32768)
	if err := windows.GetVolumePathName(pathPointer, &rootBuffer[0], uint32(len(rootBuffer))); err != nil {
		return "", "", "", err
	}
	root = windows.UTF16ToString(rootBuffer)
	rootPointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", "", "", err
	}
	volumeBuffer := make([]uint16, 64)
	if err := windows.GetVolumeNameForVolumeMountPoint(rootPointer, &volumeBuffer[0], uint32(len(volumeBuffer))); err != nil {
		return "", "", "", err
	}
	volumeID = strings.TrimSuffix(windows.UTF16ToString(volumeBuffer), `\`)
	deviceID, err = volumeDeviceID(root)
	if err != nil {
		return "", "", "", err
	}
	return root, volumeID, deviceID, nil
}

func volumeDeviceID(root string) (string, error) {
	volumePath := `\\.\` + strings.TrimSuffix(root, `\`)
	pointer, err := windows.UTF16PtrFromString(volumePath)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pointer, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]byte, 4096)
	var returned uint32
	if err := windows.DeviceIoControl(handle, ioctlVolumeGetVolumeDiskExtents, nil, 0, &buffer[0], uint32(len(buffer)), &returned, nil); err != nil {
		return "", err
	}
	if returned < 32 {
		return "", errors.New("volume disk extent response is truncated")
	}
	count := int(binary.LittleEndian.Uint32(buffer[:4]))
	if count < 1 || 8+count*24 > int(returned) {
		return "", errors.New("volume disk extent response is invalid")
	}
	disks := make([]uint32, 0, count)
	for index := 0; index < count; index++ {
		disks = append(disks, binary.LittleEndian.Uint32(buffer[8+index*24:]))
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i] < disks[j] })
	parts := make([]string, len(disks))
	for index, disk := range disks {
		parts[index] = fmt.Sprintf("disk:%d", disk)
	}
	return strings.Join(parts, ","), nil
}

func splitUTF16List(values []uint16) []string {
	var result []string
	start := 0
	for index, value := range values {
		if value != 0 {
			continue
		}
		if index > start {
			result = append(result, syscall.UTF16ToString(values[start:index]))
		}
		start = index + 1
	}
	return result
}
