package localallocation

import (
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OPEN_REPARSE_POINT + attributes-only access avoids reading/recalling bytes.
// Cloud tags are allowed; name-surrogate junctions and other providers are not.
func extendedPath(path string) string {
	if !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	return path
}

func probe(path string) (metadata, error) {
	p, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return metadata{}, err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return metadata{}, err
	}
	defer windows.CloseHandle(h)
	return handleMetadata(h)
}

func openDirectory(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	meta, err := handleMetadata(h)
	if err != nil || !meta.directory {
		windows.CloseHandle(h)
		if err == nil {
			err = errors.New("allocation: not a directory")
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func handleMetadata(h windows.Handle) (metadata, error) {
	var err error
	var tag struct{ Attributes, Tag uint32 }
	if err = windows.GetFileInformationByHandleEx(h, 9, (*byte)(unsafe.Pointer(&tag)), uint32(unsafe.Sizeof(tag))); err != nil {
		return metadata{}, err
	}
	if tag.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && tag.Tag&^0x0000f000 != 0x9000001a {
		return metadata{}, errors.New("allocation: unsupported reparse point")
	}
	dir := tag.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if dir && tag.Attributes&(0x00400000|0x00040000|windows.FILE_ATTRIBUTE_OFFLINE) != 0 {
		return metadata{}, errors.New("allocation: directory recall refused")
	}
	var standard struct {
		Allocation, End   int64
		Links             uint32
		Delete, Directory byte
		Pad               [2]byte
	}
	if err = windows.GetFileInformationByHandleEx(h, 1, (*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))); err != nil {
		return metadata{}, err
	}
	// Hardlinks have ambiguous per-server ownership. Do not double-count them
	// as a complete result; report the affected measurement as partial instead.
	if !dir && standard.Links > 1 {
		return metadata{}, errors.New("allocation: shared hardlink")
	}
	size := standard.Allocation
	if !dir && tag.Attributes&(windows.FILE_ATTRIBUTE_SPARSE_FILE|windows.FILE_ATTRIBUTE_COMPRESSED) != 0 {
		var compressed struct {
			Bytes                int64
			Format               uint16
			Unit, Chunk, Cluster byte
			Reserved             [3]byte
		}
		if err = windows.GetFileInformationByHandleEx(h, 8, (*byte)(unsafe.Pointer(&compressed)), uint32(unsafe.Sizeof(compressed))); err != nil {
			return metadata{}, err
		}
		size = compressed.Bytes
	}
	var basic windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &basic); err != nil {
		return metadata{}, err
	}
	if size < 0 {
		return metadata{}, errors.New("allocation: invalid size")
	}
	return metadata{bytes: size, directory: dir, stamp: basic.LastWriteTime.Nanoseconds()}, nil
}
