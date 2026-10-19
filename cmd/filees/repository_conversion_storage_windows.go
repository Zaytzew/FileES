//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"filees/pkg/cloudfiles"
	"golang.org/x/sys/windows"
)

const conversionEvidenceLimit = 40 << 20

type conversionObject struct {
	Path      string
	ID        string
	Directory bool
	Size      int64
	SHA256    string
	// Canonical optional inventory, kept comparable for immutable delete receipts.
	Streams           string `json:",omitempty"`
	Local             bool
	DACL              string
	Attributes        uint32
	Created, Modified windows.Filetime
}
type conversionTreePin struct {
	Objects []conversionObject
	Files   map[string]*os.File
	Streams map[string][]*os.File
}

func (p *conversionTreePin) close() {
	if p != nil {
		for _, f := range p.Files {
			_ = f.Close()
		}
		for _, files := range p.Streams {
			for _, f := range files {
				_ = f.Close()
			}
		}
		p.Files = nil
		p.Streams = nil
	}
}

func conversionID(info windows.ByHandleFileInformation) string {
	return moveObjectID(anchorReleaseID(info))
}

// Metadata admission BEFORE a data handle: never recall an offline/cloud file.
// Read handles deny all data writers, including already-open incompatible CAD
// handles. Share-delete allows our parent rename, not proof of stable paths;
// each later lookup must still match its recorded FileID and pinned ancestor.
func conversionOpen(path string, access, share uint32) (*os.File, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	wide, err := windows.UTF16PtrFromString(anchorReleaseNativePath(path))
	if err != nil {
		return nil, info, err
	}
	meta, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, info, &os.PathError{Op: "conversion metadata", Path: path, Err: err}
	}
	err = windows.GetFileInformationByHandle(meta, &info)
	windows.CloseHandle(meta)
	forbidden := uint32(windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_OFFLINE | windows.FILE_ATTRIBUTE_ENCRYPTED | 0x00040000 | 0x00400000)
	if err == nil && (info.FileAttributes&forbidden != 0 || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && cloudfiles.IsSyncRoot(path)) {
		err = errors.New("conversion refuses reparse, hardlink, offline, encrypted or nested sync-root objects")
	}
	if err != nil {
		return nil, info, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		share &^= windows.FILE_SHARE_DELETE // no directory junction/name swap while traversing
	}
	h, err := windows.CreateFile(wide, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, info, &os.PathError{Op: "conversion data pin", Path: path, Err: err}
	}
	var actual windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &actual)
	if err == nil && (conversionID(actual) != conversionID(info) || actual.FileAttributes&forbidden != 0 || actual.NumberOfLinks != 1) {
		err = errors.New("conversion object changed during admission")
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, info, err
	}
	return os.NewFile(uintptr(h), path), actual, nil
}

func conversionFileHash(ctx context.Context, f *os.File, size int64) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hashAnchorRelease(ctx, f, nil, size)
}

func conversionPinTree(ctx context.Context, root string) (*conversionTreePin, error) {
	p := &conversionTreePin{Files: map[string]*os.File{}, Streams: map[string][]*os.File{}}
	var walk func(string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(p.Objects) >= 100000 {
			return errors.New("conversion tree exceeds object limit")
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		access := uint32(windows.GENERIC_READ)
		if rel == "." {
			access |= windows.DELETE
		}
		f, info, err := conversionOpen(path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
		if err != nil {
			return fmt.Errorf("pin %s: %w", rel, err)
		}
		p.Files[rel] = f
		node := conversionObject{Path: rel, ID: conversionID(info), Directory: info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, Size: int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)), Attributes: info.FileAttributes, Created: info.CreationTime, Modified: info.LastWriteTime}
		node.DACL, err = conversionDACL(f)
		if err != nil {
			return err
		}
		var streams []*os.File
		node.Streams, streams, err = conversionPinStreams(ctx, f)
		p.Streams[rel] = streams
		if err != nil {
			return fmt.Errorf("inspect %q: %w", rel, err)
		}
		if node.Streams != "" && (rel == ".svn" || strings.HasPrefix(rel, ".svn/") || rel == ".filees" || strings.HasPrefix(rel, ".filees/")) {
			return errors.New("conversion refuses alternate streams on private engine metadata")
		}
		if !node.Directory {
			node.SHA256, err = conversionFileHash(ctx, f, node.Size)
			if err != nil {
				return err
			}
		}
		p.Objects = append(p.Objects, node)
		if node.Directory {
			entries, err := readPinnedMoveDirectory(windows.Handle(f.Fd()), path)
			if err != nil {
				return err
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				name := entry.Name()
				if name == "" || strings.ContainsAny(name, "\\/:\x00") {
					return errors.New("conversion directory contains an unsafe name")
				}
				child := name
				if rel != "." {
					child = rel + "/" + name
				}
				if strings.EqualFold(name, ".svn") && child != ".svn" {
					return errors.New("conversion refuses a nested working copy")
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		p.close()
		return nil, err
	}
	sort.Slice(p.Objects, func(i, j int) bool { return p.Objects[i].Path < p.Objects[j].Path })
	return p, nil
}

// FILE_STREAM_INFO requires 8-byte alignment, which a stack []byte does not
// promise. Use a uint64 backing allocation for all variable Windows records.
func conversionAlignedBuffer(size int) []byte {
	words := make([]uint64, (size+7)/8)
	return unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)[:size]
}

func conversionSameObject(got, want conversionObject) bool {
	return got.Path == want.Path && got.ID == want.ID && got.Streams == want.Streams && conversionSameDACL(got.DACL, want.DACL) && got.Directory == want.Directory && (got.Directory || got.Size == want.Size && got.SHA256 == want.SHA256 && got.Attributes == want.Attributes && got.Modified == want.Modified)
}

// AUTO_INHERITED/AUTO_INHERIT_REQ are bookkeeping, not access permissions.
// Keep protection and every ordered ACE (including inheritance flags) exact.
func conversionSameDACL(a, b string) bool {
	clean := func(s string) string {
		at := strings.Index(s, "(")
		if at < 0 {
			at = len(s)
		}
		prefix := strings.ReplaceAll(strings.ReplaceAll(s[:at], "AI", ""), "AR", "")
		return prefix + s[at:]
	}
	return clean(a) == clean(b)
}

func conversionDACL(file *os.File) (string, error) {
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

// DACL-only clone: no owner/SACL privilege escalation or best-effort fallback.
func conversionSetMetadata(file *os.File, node conversionObject) error {
	sd, err := windows.SecurityDescriptorFromString(node.DACL)
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	flags := windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	if control&windows.SE_DACL_PROTECTED != 0 {
		flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err = windows.SetKernelObjectSecurity(windows.Handle(file.Fd()), windows.SECURITY_INFORMATION(flags), sd); err != nil {
		return err
	}
	got, err := conversionDACL(file)
	if err != nil {
		return err
	}
	if !conversionSameDACL(got, node.DACL) {
		return errors.New("preserved access permissions differ")
	}
	if err = windows.SetFileTime(windows.Handle(file.Fd()), &node.Created, nil, &node.Modified); err != nil {
		return err
	}
	type basicInfo struct {
		Created, Accessed, Written, Changed int64
		Attributes                          uint32
		Padding                             uint32
	}
	attrs := node.Attributes & (windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE)
	if attrs == 0 {
		attrs = windows.FILE_ATTRIBUTE_NORMAL
	}
	basic := basicInfo{Attributes: attrs}
	return windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
}

func conversionCheckMetadata(file *os.File, info windows.ByHandleFileInformation, node conversionObject) error {
	acl, err := conversionDACL(file)
	if err != nil {
		return err
	}
	mask := uint32(windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE)
	if !conversionSameDACL(acl, node.DACL) || info.FileAttributes&mask != node.Attributes&mask || !node.Directory && (info.LastWriteTime != node.Modified || info.CreationTime != node.Created) {
		return errors.New("preserved local metadata changed")
	}
	return nil
}

// No-replace rename on the checked handle, not a pathname closed before rename.
func conversionRename(f *os.File, target string) error {
	name, err := windows.UTF16FromString(anchorReleaseNativePath(target))
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	type renameInfo struct {
		Replace byte
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	offset := unsafe.Offsetof(renameInfo{}.Name)
	// SetFileInformationByHandle's Win32 pathname conversion also requires
	// the terminating WCHAR; FileNameLength itself excludes that terminator.
	raw := conversionAlignedBuffer(int(offset) + 2*(len(name)+1))
	info := (*renameInfo)(unsafe.Pointer(&raw[0]))
	info.Length = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&raw[offset])), len(name)), name)
	return windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileRenameInfo, &raw[0], uint32(len(raw)))
}

func conversionReadJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, conversionEvidenceLimit+1))
	if err != nil {
		return err
	}
	if len(data) > conversionEvidenceLimit {
		return errors.New("conversion evidence exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("conversion evidence has trailing data")
	}
	return nil
}

func conversionWriteJSON(path string, value any, exclusive bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > conversionEvidenceLimit {
		return errors.New("conversion evidence exceeds limit")
	}
	if exclusive {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		return errors.Join(err, f.Close())
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".conversion-receipt-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func conversionDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func conversionMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

// Ordinary files only. The source and destination ancestors stay pinned.
func conversionDelete(ctx context.Context, path string, want conversionObject) error {
	f, info, err := conversionOpen(path, windows.GENERIC_READ|windows.DELETE|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
	if err != nil {
		return err
	}
	defer f.Close()
	if conversionID(info) != want.ID || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != want.Directory {
		return errors.New("cleanup refuses a replaced object")
	}
	streams, streamPins, err := conversionPinStreams(ctx, f)
	defer conversionCloseStreams(streamPins)
	if err != nil {
		return err
	}
	if streams != want.Streams {
		return errors.New("cleanup refuses changed alternate streams")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := want
	metadata.Attributes = (metadata.Attributes &^ windows.FILE_ATTRIBUTE_READONLY) | (info.FileAttributes & windows.FILE_ATTRIBUTE_READONLY)
	if err := conversionCheckMetadata(f, info, metadata); err != nil {
		return err
	}
	if !want.Directory {
		hash, err := conversionFileHash(ctx, f, want.Size)
		if err != nil {
			return err
		}
		if hash != want.SHA256 {
			return errors.New("cleanup refuses changed bytes")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0 {
		type basicInfo struct {
			Created, Accessed, Written, Changed int64
			Attributes                          uint32
			Padding                             uint32
		}
		basic := basicInfo{Attributes: info.FileAttributes &^ windows.FILE_ATTRIBUTE_READONLY}
		if basic.Attributes == 0 {
			basic.Attributes = windows.FILE_ATTRIBUTE_NORMAL
		}
		if err := windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
			return err
		}
	}
	remove := byte(1)
	if err := windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileDispositionInfo, &remove, 1); err != nil {
		return err
	}
	// Delete-pending blocks NEW stream opens. Existing streams are still pinned
	// against writers. Re-enumerate after this barrier to catch a new ADS created
	// between the last inventory and the deletion intent; revoke disposition on
	// mismatch instead of deleting unobserved metadata.
	if err := conversionCheckStreamNames(f, want.Streams); err != nil {
		remove = 0
		return errors.Join(err, windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileDispositionInfo, &remove, 1))
	}
	return nil
}
