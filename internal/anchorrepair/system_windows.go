//go:build windows

package anchorrepair

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cloudFilter = "CldFlt"
	// errorCloudFileMetadataCorrupt is what the filter answers for an entry
	// whose reparse data it cannot parse.
	errorCloudFileMetadataCorrupt = windows.Errno(363)
	// Every IO_REPARSE_TAG_CLOUD_n is 0x9000n01A.
	cloudTagMask, cloudTag = 0xFFFF0FFF, 0x9000001A
	fsctlGetReparsePoint   = 0x000900A8
	syncRootInfoProvider   = 2 // CF_SYNC_ROOT_INFO_PROVIDER
	fileDispositionInfo    = 4 // FILE_INFO_BY_HANDLE_CLASS
	hresultAccessDenied    = 0x80070005
	hresultElevationNeeded = 0x800702E4
)

var (
	fltlib           = windows.NewLazySystemDLL("fltlib.dll")
	procFilterDetach = fltlib.NewProc("FilterDetach")
	procFilterAttach = fltlib.NewProc("FilterAttach")
	cldapi           = windows.NewLazySystemDLL("cldapi.dll")
	procSyncRootInfo = cldapi.NewProc("CfGetSyncRootInfoByPath")
)

// Windows is the real System.
type Windows struct{}

func (Windows) SyncRootProvider(root string) (string, error) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	// CF_SYNC_ROOT_PROVIDER_INFO: status, then two fixed UTF-16 names.
	var info struct {
		Status  uint32
		Name    [256]uint16
		Version [256]uint16
	}
	var returned uint32
	if err := procSyncRootInfo.Find(); err != nil {
		return "", err
	}
	hr, _, _ := procSyncRootInfo.Call(uintptr(unsafe.Pointer(path)), syncRootInfoProvider,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&returned)))
	if int32(hr) < 0 {
		return "", fmt.Errorf("sync root information: 0x%08x", uint32(hr))
	}
	return windows.UTF16ToString(info.Name[:]), nil
}

func (Windows) List(directory string) ([]Entry, error) {
	pattern, err := windows.UTF16PtrFromString(extended(directory) + `\*`)
	if err != nil {
		return nil, err
	}
	var data windows.Win32finddata
	handle, err := windows.FindFirstFile(pattern, &data)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	defer windows.FindClose(handle)
	var entries []Entry
	for {
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." {
			reparse := data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
			entries = append(entries, Entry{
				Name:      name,
				Directory: data.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
				Reparse:   reparse,
				Cloud:     reparse && data.Reserved0&cloudTagMask == cloudTag,
			})
		}
		if err := windows.FindNextFile(handle, &data); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return entries, nil
			}
			return nil, err
		}
	}
}

// open reaches the entry itself: a reparse point is not followed and a
// placeholder is not hydrated.
func open(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(extended(path))
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

func (Windows) Unreadable(path string) (bool, error) {
	handle, err := open(path, windows.FILE_READ_ATTRIBUTES)
	if err == nil {
		_ = windows.CloseHandle(handle)
		return false, nil
	}
	if errors.Is(err, errorCloudFileMetadataCorrupt) {
		return true, nil
	}
	return false, err
}

func filter(proc *windows.LazyProc, volume string, extra ...uintptr) error {
	if err := proc.Find(); err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString(cloudFilter)
	target, err := windows.UTF16PtrFromString(volume)
	if err != nil {
		return err
	}
	args := append([]uintptr{uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(target)), 0}, extra...)
	hr, _, _ := proc.Call(args...)
	switch uint32(hr) {
	case 0:
		return nil
	case hresultAccessDenied, hresultElevationNeeded:
		return fmt.Errorf("%w (0x%08x)", ErrElevationNeeded, uint32(hr))
	}
	if int32(hr) < 0 {
		return fmt.Errorf("cloud files filter on %s: 0x%08x", volume, uint32(hr))
	}
	return nil
}

func (Windows) Detach(volume string) error { return filter(procFilterDetach, volume) }

// Attach uses the filter's default instance, as `fltmc attach CldFlt X:` does.
func (Windows) Attach(volume string) error { return filter(procFilterAttach, volume, 0, 0) }

func finalPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 1024)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if int(n) < len(buffer) {
			return windows.UTF16ToString(buffer[:n]), nil
		}
		buffer = make([]uint16, n+1)
	}
}

func (Windows) RemoveCloudEntry(root, path string, directory bool) error {
	rootHandle, err := open(root, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return fmt.Errorf("open folder: %w", err)
	}
	rootFinal, err := finalPath(rootHandle)
	_ = windows.CloseHandle(rootHandle)
	if err != nil {
		return err
	}
	handle, err := open(path, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	// The handle, not the name, decides: what was opened must be inside the
	// folder, be a cloud files reparse point, and be the kind that was seen.
	final, err := finalPath(handle)
	if err != nil {
		return err
	}
	if len(final) <= len(rootFinal)+1 || !strings.EqualFold(final[:len(rootFinal)], rootFinal) || final[len(rootFinal)] != '\\' {
		return errors.New("entry resolves outside the folder")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if (information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("entry changed kind")
	}
	// REPARSE_DATA_BUFFER begins with the tag; nothing else is needed.
	buffer := make([]byte, 16*1024)
	var returned uint32
	if err := windows.DeviceIoControl(handle, fsctlGetReparsePoint, nil, 0, &buffer[0], uint32(len(buffer)), &returned, nil); err != nil {
		return fmt.Errorf("not a reparse point: %w", err)
	}
	if returned < 4 || *(*uint32)(unsafe.Pointer(&buffer[0]))&cloudTagMask != cloudTag {
		return errors.New("not a cloud files entry")
	}
	remove := struct{ Delete uint8 }{1}
	if err := windows.SetFileInformationByHandle(handle, fileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), uint32(unsafe.Sizeof(remove))); err != nil {
		return err
	}
	return nil
}

func extended(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	return `\\?\` + filepath.Clean(path)
}

// Elevated reports whether this process holds an elevated token.
func Elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
