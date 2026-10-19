//go:build windows && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	"filees/pkg/localrepo"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type conversionStream struct {
	Name   string
	Size   int64
	SHA256 string
}

func conversionStreamName(name string) bool {
	if !strings.HasPrefix(name, ":") || !strings.HasSuffix(name, ":$DATA") {
		return false
	}
	value := strings.TrimSuffix(name[1:], ":$DATA")
	return value != "" && len(value) <= 1020 && !strings.ContainsAny(value, "\\/:\x00")
}

func conversionStreamList(file *os.File) ([]conversionStream, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return nil, err
	}
	var raw []byte
	for size := 4096; size <= 1<<20; size *= 2 {
		raw = conversionAlignedBuffer(size)
		err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileStreamInfo, &raw[0], uint32(len(raw)))
		if errors.Is(err, windows.ERROR_HANDLE_EOF) {
			if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				return nil, nil
			}
			return nil, err
		}
		if errors.Is(err, windows.ERROR_MORE_DATA) || errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return conversionParseStreams(raw)
	}
	return nil, errors.New("conversion stream inventory exceeds limit")
}
func conversionParseStreams(raw []byte) ([]conversionStream, error) {
	out := []conversionStream{}
	seen := map[string]bool{}
	for offset := 0; ; {
		if offset+24 > len(raw) {
			return nil, errors.New("invalid file-stream observation")
		}
		next := *(*uint32)(unsafe.Pointer(&raw[offset]))
		length := *(*uint32)(unsafe.Pointer(&raw[offset+4]))
		size := *(*int64)(unsafe.Pointer(&raw[offset+8]))
		if length == 0 || length%2 != 0 || uint64(offset)+24+uint64(length) > uint64(len(raw)) || size < 0 {
			return nil, errors.New("incomplete file-stream observation")
		}
		words := unsafe.Slice((*uint16)(unsafe.Pointer(&raw[offset+24])), int(length/2))
		name := windows.UTF16ToString(words)
		// Reject embedded terminators/non-canonical UTF-16, not a shortened path.
		encoded, err := windows.UTF16FromString(name)
		if err != nil || len(encoded)-1 != len(words) {
			return nil, errors.New("unsafe file-stream name")
		}
		for i := range words {
			if words[i] != encoded[i] {
				return nil, errors.New("invalid stream unicode")
			}
		}
		if name != "::$DATA" {
			if !conversionStreamName(name) || seen[strings.ToLower(name)] || len(out) >= 128 {
				return nil, errors.New("unsupported or ambiguous NTFS stream")
			}
			seen[strings.ToLower(name)] = true
			out = append(out, conversionStream{Name: name, Size: size})
		}
		if next == 0 {
			break
		}
		if next%8 != 0 || next < uint32(24+length) || uint64(offset)+uint64(next)+24 > uint64(len(raw)) {
			return nil, errors.New("invalid file-stream chain")
		}
		offset += int(next)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func conversionStreamManifest(list []conversionStream) string {
	if len(list) == 0 {
		return ""
	}
	raw, _ := json.Marshal(list)
	return string(raw)
}
func conversionDecodeStreams(raw string) ([]conversionStream, error) {
	if raw == "" {
		return nil, nil
	}
	var list []conversionStream
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	if len(list) > 128 || conversionStreamManifest(list) != raw {
		return nil, errors.New("non-canonical stream receipt")
	}
	seen := map[string]bool{}
	for i, s := range list {
		if !conversionStreamName(s.Name) || s.Size < 0 || len(s.SHA256) != 64 || strings.Trim(s.SHA256, "0123456789abcdef") != "" || seen[strings.ToLower(s.Name)] || i > 0 && list[i-1].Name >= s.Name {
			return nil, errors.New("invalid stream receipt")
		}
		seen[strings.ToLower(s.Name)] = true
	}
	return list, nil
}
func conversionCloseStreams(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// The base and its ancestors remain pinned; check the same FileID after open.
// OPEN_REPARSE_POINT opens only the named stream, never the placeholder's data.
func conversionOpenStream(base *os.File, name string, access, disposition uint32) (*os.File, error) {
	return conversionOpenStreamShared(base, name, access, disposition, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
}
func conversionOpenStreamShared(base *os.File, name string, access, disposition, share uint32) (*os.File, error) {
	if !conversionStreamName(name) {
		return nil, errors.New("unsafe stream path")
	}
	var before windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(base.Fd()), &before); err != nil {
		return nil, err
	}
	wide, err := windows.UTF16PtrFromString(anchorReleaseNativePath(base.Name()) + name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(wide, access, share, nil, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	var actual windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &actual)
	if err == nil && conversionID(before) != conversionID(actual) {
		err = errors.New("stream base was replaced")
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), base.Name()+name), nil
}
func conversionPinStreams(ctx context.Context, base *os.File, keepBase ...bool) (string, []*os.File, error) {
	list, err := conversionStreamList(base)
	if err != nil {
		return "", nil, err
	}
	var pins []*os.File
	fail := func(err error) (string, []*os.File, error) { conversionCloseStreams(pins); return "", nil, err }
	for i := range list {
		s := &list[i]
		share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_DELETE)
		if len(keepBase) > 0 && keepBase[0] {
			share = windows.FILE_SHARE_READ
		}
		file, err := conversionOpenStreamShared(base, s.Name, windows.GENERIC_READ, windows.OPEN_EXISTING, share)
		if err != nil {
			return fail(err)
		}
		pins = append(pins, file)
		s.SHA256, err = conversionFileHash(ctx, file, s.Size)
		if err != nil {
			return fail(err)
		}
	}
	manifest := conversionStreamManifest(list)
	if err := conversionCheckStreamNames(base, manifest); err != nil {
		return fail(err)
	}
	return manifest, pins, nil
}
func conversionCheckStreamNames(base *os.File, manifest string) error {
	expected, err := conversionDecodeStreams(manifest)
	if err != nil {
		return err
	}
	actual, err := conversionStreamList(base)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return errors.New("NTFS stream set changed")
	}
	for i := range actual {
		if actual[i].Name != expected[i].Name || actual[i].Size != expected[i].Size {
			return errors.New("NTFS stream identity or size changed")
		}
	}
	return nil
}
func conversionCheckStreams(ctx context.Context, base *os.File, manifest string) error {
	got, pins, err := conversionPinStreams(ctx, base)
	defer conversionCloseStreams(pins)
	if err != nil {
		return err
	}
	if got != manifest {
		return errors.New("NTFS stream bytes changed")
	}
	return nil
}

// A private staging file has its own durable FileID receipt. Only it may be
// rewritten after a partial copy; unexpected streams are never removed.
func (f *nativeConversionEffects) copyLocalStreams(ctx context.Context, node conversionObject, target *os.File) error {
	expected, err := conversionDecodeStreams(node.Streams)
	if err != nil {
		return err
	}
	actual, err := conversionStreamList(target)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, s := range expected {
		allowed[s.Name] = true
	}
	for _, s := range actual {
		if !allowed[s.Name] {
			return errors.New("unexpected stream in private staging")
		}
	}
	source := f.old.Files[node.Path]
	if source == nil {
		return errors.New("stream source not pinned")
	}
	for _, s := range expected {
		in, err := conversionOpenStream(source, s.Name, windows.GENERIC_READ, windows.OPEN_EXISTING)
		if err != nil {
			return err
		}
		out, err := conversionOpenStream(target, s.Name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.OPEN_ALWAYS)
		if err != nil {
			in.Close()
			return err
		}
		err = out.Truncate(0)
		if err == nil {
			var hash string
			hash, err = hashAnchorRelease(ctx, in, out, s.Size)
			if err == nil && hash != s.SHA256 {
				err = errors.New("source stream differs from plan")
			}
		}
		if err == nil {
			err = out.Sync()
		}
		err = errors.Join(err, in.Close(), out.Close())
		if err != nil {
			return err
		}
	}
	return conversionCheckStreams(ctx, target, node.Streams)
}

type conversionStreamReceipt struct{ Operation, Path, BaseID, Name, Stage, Phase string }

func (f *nativeConversionEffects) streamReceipt(path, name string) string {
	digest, _ := conversionDigest([]string{path, name})
	return f.receipt("stream-" + digest)
}

func conversionStreamBase(path string, sealed bool) (*os.File, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	wide, err := windows.UTF16PtrFromString(anchorReleaseNativePath(path))
	if err != nil {
		return nil, info, err
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.FILE_WRITE_ATTRIBUTES | windows.WRITE_DAC)
	if sealed {
		share = 0
		access = windows.FILE_READ_ATTRIBUTES | windows.WRITE_DAC
	}
	h, err := windows.CreateFile(wide, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, info, err
	}
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, info, err
	}
	return os.NewFile(uintptr(h), path), info, nil
}

func (f *nativeConversionEffects) preserveRemoteStreams(ctx context.Context, r localrepo.ConversionRecord, node conversionObject) error {
	expected, err := conversionDecodeStreams(node.Streams)
	if err != nil {
		return err
	}
	target := filepath.Join(r.SourcePath, filepath.FromSlash(node.Path))
	base, info, err := conversionStreamBase(target, false)
	if err != nil {
		return err
	}
	defer base.Close()
	if node.Path == "." && conversionID(info) != f.state.ID {
		return errors.New("stream root differs")
	}
	for _, s := range expected {
		if err = f.check(ctx); err != nil {
			return err
		}
		path := f.streamReceipt(node.Path, s.Name)
		var receipt conversionStreamReceipt
		err = conversionReadJSON(path, &receipt)
		if err != nil && !conversionMissing(err) {
			return err
		}
		if conversionMissing(err) {
			// Never overwrite an existing stream, even if its bytes happen to match.
			existing, openErr := conversionOpenStream(base, s.Name, windows.GENERIC_READ, windows.OPEN_EXISTING)
			if openErr == nil {
				existing.Close()
				return errors.New("destination stream already exists")
			}
			if !conversionMissing(openErr) {
				return openErr
			}
			receipt = conversionStreamReceipt{Operation: r.ID, Path: node.Path, BaseID: conversionID(info), Name: s.Name, Stage: ":filees-conversion-" + uuid.NewString() + ":$DATA", Phase: "staged"}
			if err = conversionWriteJSON(path, receipt, true); err != nil {
				return err
			}
		}
		stageUUID := strings.TrimSuffix(strings.TrimPrefix(receipt.Stage, ":filees-conversion-"), ":$DATA")
		if receipt.Operation != r.ID || receipt.Path != node.Path || receipt.BaseID != conversionID(info) || receipt.Name != s.Name || !strings.HasPrefix(receipt.Stage, ":filees-conversion-") || !conversionStreamName(receipt.Stage) || uuid.Validate(stageUUID) != nil || receipt.Stage == receipt.Name {
			return errors.New("stream receipt binding differs")
		}
		if receipt.Phase == "moving" || receipt.Phase == "complete" {
			final, openErr := conversionOpenStream(base, s.Name, windows.GENERIC_READ, windows.OPEN_EXISTING)
			if openErr == nil {
				hash, hashErr := conversionFileHash(ctx, final, s.Size)
				final.Close()
				if hashErr != nil || hash != s.SHA256 {
					return errors.Join(hashErr, errors.New("destination stream changed"))
				}
				stage, stageErr := conversionOpenStream(base, receipt.Stage, windows.GENERIC_READ, windows.OPEN_EXISTING)
				if stageErr == nil {
					stage.Close()
					return errors.New("duplicate stream custody")
				}
				if !conversionMissing(stageErr) {
					return stageErr
				}
				receipt.Phase = "complete"
				if err = conversionWriteJSON(path, receipt, false); err != nil {
					return err
				}
				continue
			}
			if receipt.Phase == "complete" || !conversionMissing(openErr) {
				return openErr
			}
		} else if receipt.Phase != "staged" {
			return errors.New("invalid stream receipt phase")
		}
		stage, err := conversionOpenStream(base, receipt.Stage, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.OPEN_ALWAYS)
		if err != nil {
			return err
		}
		if receipt.Phase == "staged" {
			source := f.old.Files[node.Path]
			if source == nil {
				stage.Close()
				return errors.New("source stream base not pinned")
			}
			in, err := conversionOpenStream(source, s.Name, windows.GENERIC_READ, windows.OPEN_EXISTING)
			if err != nil {
				stage.Close()
				return err
			}
			err = stage.Truncate(0)
			var hash string
			if err == nil {
				hash, err = hashAnchorRelease(ctx, in, stage, s.Size)
			}
			in.Close()
			if err == nil && hash != s.SHA256 {
				err = errors.New("source stream changed before copying")
			}
			if err == nil {
				err = stage.Sync()
			}
			if err != nil {
				stage.Close()
				return err
			}
		} else {
			hash, err := conversionFileHash(ctx, stage, s.Size)
			if err != nil || hash != s.SHA256 {
				stage.Close()
				return errors.Join(err, errors.New("staged stream changed"))
			}
		}
		receipt.Phase = "moving"
		err = conversionWriteJSON(path, receipt, false)
		if err == nil {
			err = f.cut("stream-ready:" + node.Path + s.Name)
		}
		if err == nil {
			err = conversionRenameStream(stage, s.Name)
		}
		stage.Close()
		if err != nil {
			return err
		}
		if err = f.cut("stream-moved:" + node.Path + s.Name); err != nil {
			return err
		}
		receipt.Phase = "complete"
		if err = conversionWriteJSON(path, receipt, false); err != nil {
			return err
		}
	}
	// Restore the placeholder's own timestamps, not the full WC's bookkeeping.
	if err = windows.SetFileTime(windows.Handle(base.Fd()), &info.CreationTime, nil, &info.LastWriteTime); err != nil {
		return err
	}
	return conversionCheckStreams(ctx, base, node.Streams)
}

func conversionRenameStream(file *os.File, name string) error {
	if !conversionStreamName(name) {
		return errors.New("unsafe stream rename")
	}
	wide, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	type renameInfo struct {
		Replace byte
		Root    windows.Handle
		Length  uint32
		Name    [1]uint16
	}
	offset := unsafe.Offsetof(renameInfo{}.Name)
	raw := conversionAlignedBuffer(int(offset) + 2*len(wide))
	info := (*renameInfo)(unsafe.Pointer(&raw[0]))
	info.Length = uint32(2 * (len(wide) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&raw[offset])), len(wide)), wide)
	return windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileRenameInfo, &raw[0], uint32(len(raw)))
}

func (f *nativeConversionEffects) pinPreservedStreams(ctx context.Context, r localrepo.ConversionRecord) ([]*os.File, error) {
	var pins []*os.File
	fail := func(err error) ([]*os.File, error) { conversionCloseStreams(pins); return nil, err }
	for _, node := range f.plan.Objects {
		if node.Local || node.Streams == "" {
			continue
		}
		target := filepath.Join(r.SourcePath, filepath.FromSlash(node.Path))
		parent := filepath.Dir(target)
		if node.Path == "." {
			parent = r.SourcePath
		}
		guards, err := guardAnchorReleaseDirectories(parent, r.SourcePath)
		if err != nil {
			return fail(err)
		}
		f.guards = append(f.guards, guards...)
		base, info, err := conversionStreamBase(target, true)
		if err != nil {
			return fail(err)
		}
		pins = append(pins, base)
		list, err := conversionDecodeStreams(node.Streams)
		if err != nil {
			return fail(err)
		}
		for _, s := range list {
			var receipt conversionStreamReceipt
			if err = conversionReadJSON(f.streamReceipt(node.Path, s.Name), &receipt); err != nil {
				return fail(err)
			}
			if receipt.Operation != r.ID || receipt.BaseID != conversionID(info) || receipt.Path != node.Path || receipt.Name != s.Name || receipt.Phase != "complete" {
				return fail(errors.New("preserved stream receipt differs"))
			}
		}
		// Named-stream deny-delete protects the ENTIRE target file from rename
		// or unlink. Attribute-only base handles do not provide this custody.
		got, streams, err := conversionPinStreams(ctx, base, true)
		pins = append(pins, streams...)
		if err != nil {
			return fail(err)
		}
		if got != node.Streams {
			return fail(fmt.Errorf("preserved streams changed at %q", node.Path))
		}
	}
	return pins, nil
}

// ADS changes invalidate Shell's clean bit but do not publish or hydrate data.
// A fresh bound remote/size/zero-data observation precedes the stream custody
// proof, and the connected helper compares that SAME nonzero USN under a
// primary write-excluding APPEND-access handle without issuing data I/O.
// This is not the ordinary local-file/ALWAYS_FULL state conversion.
func (f *nativeConversionEffects) acknowledgeStreamMetadata(ctx context.Context, r localrepo.ConversionRecord) error {
	var nodes []conversionObject
	for _, node := range f.plan.Objects {
		if node.Local || node.Streams == "" {
			continue
		}
		if node.Directory {
			return errors.New("versioned directory streams require a separate child-state proof")
		}
		nodes = append(nodes, node)
	}
	var pending []struct {
		node  conversionObject
		state cfapiAnswer
	}
	for start := 0; start < len(nodes); start += 64 {
		end := start + 64
		if end > len(nodes) {
			end = len(nodes)
		}
		if err := f.check(ctx); err != nil {
			return err
		}
		var input strings.Builder
		for _, node := range nodes[start:end] {
			input.WriteString(node.Path + "\n")
		}
		answer, err := f.manager.call(ctx, input.String(), "tree-states", "--root", r.SourcePath)
		if err != nil || !answer.OK || len(answer.States) != end-start {
			return errors.Join(err, errors.New("stream placeholder state unavailable"))
		}
		for i, s := range answer.States {
			node := nodes[start+i]
			if !s.OK || !s.Placeholder || s.Directory || s.Local || s.Identity != node.Path || s.Size == nil || *s.Size != node.Size || s.OnDisk != 0 || s.Modified != 0 {
				return errors.New("stream placeholder is not the unchanged remote file")
			}
			if s.InSync {
				continue
			}
			usn, err := strconv.ParseInt(s.USN, 10, 64)
			if err != nil || usn <= 0 {
				return errors.New("stream metadata acknowledgement lacks a USN")
			}
			pending = append(pending, struct {
				node  conversionObject
				state cfapiAnswer
			}{node, s})
		}
	}
	if len(pending) == 0 {
		return nil
	}
	// All observations precede ONE inventory/hash/pin pass, not N repeated
	// full passes for N marked files. The pins live through every typed ACK.
	pins, err := f.pinPreservedStreams(ctx, r)
	if err != nil {
		return err
	}
	defer conversionCloseStreams(pins)
	for _, item := range pending {
		if err := f.check(ctx); err != nil {
			return err
		}
		node, s := item.node, item.state
		base, info, err := conversionStreamBase(filepath.Join(r.SourcePath, filepath.FromSlash(node.Path)), true)
		if err == nil {
			if f.bridge == nil {
				err = errors.New("stream metadata acknowledgement has no connected provider")
			} else {
				err = f.bridge.connectedCommand(ctx, "stream-set", "stream-set", node.Path, s.USN, conversionID(info), node.Path, strconv.FormatInt(node.Size, 10))
			}
			base.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}
