//go:build windows && !nocfapi

package main

// Private release evidence only. No startup hook, exclusion, restoration or
// backup retirement is wired here. A future coordinator must hold wcOpMu and
// inspect actual effects before acting after an uncertain receipt. A phase is
// a persisted REQUEST, never evidence that its external effect happened.
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
	"strconv"
	"strings"

	"filees/pkg/cloudfiles"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type anchorReleaseIdentity struct {
	ServerID string
	RepoID   string
	RepoURL  string
	WC       string
	Rel      string
	Revision int64
}

type anchorReleaseFileID struct {
	Volume uint32
	Index  uint64
}

type anchorReleaseRecord struct {
	Version  int
	ID       string
	Identity anchorReleaseIdentity
	Root     anchorReleaseFileID
	Source   anchorReleaseFileID
	USN      string
	Size     int64
	SHA256   string
	Phase    string
}

type anchorReleaseJournal struct {
	store    string
	identity anchorReleaseIdentity
	id       string
}

var anchorReleasePhases = [...]string{"retained", "prepare_requested", "exclude_requested", "restore_requested"}

func sameAnchorReleaseState(a, b cfapiAnswer) bool {
	return a.OK == b.OK && a.Placeholder == b.Placeholder && a.Local == b.Local &&
		a.Partial == b.Partial && a.InSync == b.InSync && a.USN == b.USN
}

func validateAnchorReleaseIdentity(in anchorReleaseIdentity) error {
	rel, ok := cleanAnchorIdentity(in.Rel)
	if !ok || rel != in.Rel || in.ServerID == "" || in.RepoID == "" ||
		in.RepoURL == "" || in.Revision < 1 || !filepath.IsAbs(in.WC) || filepath.Clean(in.WC) != in.WC {
		return errors.New("invalid Explorer release identity")
	}
	return nil
}

func validateAnchorReleaseStore(store, wc string) error {
	if !filepath.IsAbs(store) || filepath.Clean(store) != store || strings.HasPrefix(store, `\\`) {
		return errors.New("release storage needs an explicit canonical local directory")
	}
	rel, err := filepath.Rel(wc, store)
	if err != nil {
		if !strings.EqualFold(filepath.VolumeName(store), filepath.VolumeName(wc)) {
			return nil // A different local volume is outside the WC.
		}
		return err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("release copy must be outside the WC")
	}
	return nil
}

func (j *anchorReleaseJournal) validate() error {
	if err := validateAnchorReleaseIdentity(j.identity); err != nil {
		return err
	}
	if err := validateAnchorReleaseStore(j.store, j.identity.WC); err != nil {
		return err
	}
	if parsed, err := uuid.Parse(j.id); err != nil || parsed.String() != j.id {
		return errors.New("invalid release transaction ID")
	}
	return nil
}

func anchorReleaseID(info windows.ByHandleFileInformation) anchorReleaseFileID {
	return anchorReleaseFileID{info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
}

// Guards every directory component through the final directory. Junctions,
// symlinks and remote paths refuse. The one admitted reparse directory is the
// exact registered WC root; helper inspection independently proves ownership.
// Reparse subdirectories remain conservative refusals in this first slice.
func guardAnchorReleaseDirectories(path, wcRoot string) ([]windows.Handle, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, `\\`) {
		return nil, errors.New("release storage needs a canonical local directory")
	}
	var paths []string
	for p := path; ; p = filepath.Dir(p) {
		paths = append(paths, p)
		if p == filepath.Dir(p) {
			break
		}
	}
	var guards []windows.Handle
	for i := len(paths) - 1; i >= 0; i-- {
		wide, err := windows.UTF16PtrFromString(paths[i])
		if err != nil {
			closeAnchorReleaseGuards(guards)
			return nil, err
		}
		// Attribute-only handles do NOT enforce rename denial on Windows
		// (native RED/GREEN). A read/list access handle does; do not enumerate
		// it or read directory contents through the Cloud Files filter.
		h, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			var info windows.ByHandleFileInformation
			err = windows.GetFileInformationByHandle(h, &info)
			if err == nil && (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
				(info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 &&
					!(strings.EqualFold(paths[i], wcRoot) && cloudfiles.IsSyncRoot(paths[i])))) {
				err = errors.New("release directory is not an ordinary directory or its exact sync root")
			}
			guards = append(guards, h)
		}
		if err != nil {
			closeAnchorReleaseGuards(guards)
			return nil, err
		}
	}
	return guards, nil
}

func closeAnchorReleaseGuards(guards []windows.Handle) {
	for i := len(guards) - 1; i >= 0; i-- {
		_ = windows.CloseHandle(guards[i])
	}
}

// Admission is metadata-only before GENERIC_READ, so no remote bytes are
// requested. The data handle then denies write and delete until it closes.
func openAnchorReleaseFile(path string, localPlaceholder bool) (*os.File, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, info, err
	}
	meta, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, info, err
	}
	defer windows.CloseHandle(meta)
	err = windows.GetFileInformationByHandle(meta, &info)
	forbidden := uint32(windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_OFFLINE | 0x00040000 | 0x00400000)
	if !localPlaceholder {
		forbidden |= windows.FILE_ATTRIBUTE_REPARSE_POINT
	}
	if err == nil && (info.FileAttributes&forbidden != 0 || info.NumberOfLinks != 1) {
		err = errors.New("release requires a single-link fully local file")
	}
	if err == nil && localPlaceholder {
		managed, _ := cloudfiles.LocalState(path)
		if !managed {
			err = errors.New("release source lacks a fully local FileES marker")
		}
	}
	if err != nil {
		return nil, info, err
	}
	h, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, info, err
	}
	var guarded windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &guarded)
	if err == nil && (anchorReleaseID(guarded) != anchorReleaseID(info) || guarded.FileAttributes&forbidden != 0 || guarded.NumberOfLinks != 1) {
		err = errors.New("release file changed during admission")
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, info, err
	}
	return os.NewFile(uintptr(h), path), guarded, nil
}

type anchorReleaseReader struct {
	ctx context.Context
	r   io.Reader
}

func (r anchorReleaseReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// SHA is over all bytes, not the file's size/mtime cache. Size+1 also rejects
// growth while reading without an unbounded allocation.
func hashAnchorRelease(ctx context.Context, src io.Reader, dst io.Writer, size int64) (string, error) {
	if size < 0 || size == int64(^uint64(0)>>1) {
		return "", errors.New("invalid release byte count")
	}
	h := sha256.New()
	writer := io.Writer(h)
	if dst != nil {
		writer = io.MultiWriter(dst, h)
	}
	n, err := io.CopyBuffer(writer, anchorReleaseReader{ctx, io.LimitReader(src, size+1)}, make([]byte, 32*1024))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", errors.New("release byte count changed")
	}
	return hex.EncodeToString(h.Sum(nil)), ctx.Err()
}

func writeAnchorReleaseRecord(dir string, index int, record anchorReleaseRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	// A failed write is retained for diagnosis; it is never a phase receipt.
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(dir, fmt.Sprintf("%02d.json", index)))
	if err != nil {
		return err
	}
	// No replacement. This is the existing Windows write-through rename pattern,
	// not a claim about storage hardware surviving power failure.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

// Caller must hold its running WC guard and obtain the revision/repo identity
// from SVN, not from Shell in-sync. inspect is the real metadata-only helper
// check for this exact root and path, repeated while the byte guard is held.
func prepareAnchorReleaseJournal(ctx context.Context, store string, identity anchorReleaseIdentity,
	inspect func(context.Context) (cfapiAnswer, error)) (*anchorReleaseJournal, error) {
	if err := validateAnchorReleaseIdentity(identity); err != nil {
		return nil, err
	}
	if inspect == nil {
		return nil, errors.New("release needs explicit storage and metadata inspection")
	}
	if err := validateAnchorReleaseStore(store, identity.WC); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceGuards, err := guardAnchorReleaseDirectories(filepath.Dir(filepath.Join(identity.WC, identity.Rel)), identity.WC)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(sourceGuards)
	storeGuards, err := guardAnchorReleaseDirectories(store, "")
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(storeGuards)
	state, err := inspect(ctx)
	if err != nil {
		return nil, err
	}
	usn, numberErr := strconv.ParseInt(state.USN, 10, 64)
	if numberErr != nil || usn <= 0 || !state.OK || !state.Placeholder || !state.Local || state.Partial || !state.InSync {
		return nil, errors.New("release source is not a clean full FileES file")
	}
	src, info, err := openAnchorReleaseFile(filepath.Join(identity.WC, identity.Rel), true)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	guarded, err := inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect guarded release source: %w", err)
	}
	if !sameAnchorReleaseState(guarded, state) {
		return nil, errors.New("release metadata changed before copying")
	}
	wide, _ := windows.UTF16PtrFromString(identity.WC)
	root, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(root)
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(root, &rootInfo); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	dir := filepath.Join(store, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	dirGuards, err := guardAnchorReleaseDirectories(dir, "")
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(dirGuards)
	dst, err := os.OpenFile(filepath.Join(dir, "data.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	size := int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
	digest, err := hashAnchorRelease(ctx, src, dst, size)
	if err == nil {
		err = dst.Sync()
	}
	err = errors.Join(err, dst.Close())
	if err != nil {
		return nil, err
	}
	after, err := inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect retained release source: %w", err)
	}
	if !sameAnchorReleaseState(after, state) {
		return nil, errors.New("release metadata changed while retaining bytes")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record := anchorReleaseRecord{Version: 1, ID: id, Identity: identity, Root: anchorReleaseID(rootInfo),
		Source: anchorReleaseID(info), USN: state.USN, Size: size, SHA256: digest, Phase: anchorReleasePhases[0]}
	if err := writeAnchorReleaseRecord(dir, 0, record); err != nil {
		return nil, err
	}
	return &anchorReleaseJournal{store: store, identity: identity, id: id}, nil
}

func readAnchorReleaseRecord(ctx context.Context, path string) (anchorReleaseRecord, error) {
	var record anchorReleaseRecord
	f, info, err := openAnchorReleaseFile(path, false)
	if err != nil {
		return record, err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > 16*1024 {
		return record, errors.New("release record exceeds size limit")
	}
	decoder := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 16*1024+1)})
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return record, errors.New("release record has trailing data")
	}
	return record, nil
}

// A fresh journal object after process restart still requires the caller's
// expected identity. Stored paths never authorize access to another WC.
func (j *anchorReleaseJournal) inspect(ctx context.Context) (anchorReleaseRecord, error) {
	if err := j.validate(); err != nil {
		return anchorReleaseRecord{}, err
	}
	dir := filepath.Join(j.store, j.id)
	guards, err := guardAnchorReleaseDirectories(dir, "")
	if err != nil {
		return anchorReleaseRecord{}, err
	}
	defer closeAnchorReleaseGuards(guards)
	record, err := readAnchorReleaseRecord(ctx, filepath.Join(dir, "00.json"))
	if err != nil {
		return record, err
	}
	if record.Version != 1 || record.ID != j.id || record.Identity != j.identity || record.Phase != anchorReleasePhases[0] ||
		record.Size < 0 || record.Size == int64(^uint64(0)>>1) || record.Source.Index == 0 || record.Root.Index == 0 {
		return record, errors.New("release evidence belongs to another transaction, WC or repository")
	}
	if usn, err := strconv.ParseInt(record.USN, 10, 64); err != nil || usn <= 0 {
		return record, errors.New("invalid retained USN")
	}
	if digest, err := hex.DecodeString(record.SHA256); err != nil || len(digest) != sha256.Size {
		return record, errors.New("invalid retained digest")
	}
	wide, _ := windows.UTF16PtrFromString(j.identity.WC)
	h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return record, err
	}
	var rootInfo windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &rootInfo)
	_ = windows.CloseHandle(h)
	if err != nil || anchorReleaseID(rootInfo) != record.Root {
		return record, errors.New("release WC root was replaced")
	}
	for i := 1; i < len(anchorReleasePhases); i++ {
		next, err := readAnchorReleaseRecord(ctx, filepath.Join(dir, fmt.Sprintf("%02d.json", i)))
		if os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			for k := i + 1; k < len(anchorReleasePhases); k++ {
				if _, err := os.Lstat(filepath.Join(dir, fmt.Sprintf("%02d.json", k))); !os.IsNotExist(err) {
					return record, errors.New("release phase history has a gap")
				}
			}
			break
		}
		if err != nil {
			return record, err
		}
		expected := record
		expected.Phase = anchorReleasePhases[i]
		if next != expected {
			return record, errors.New("release phase identity changed")
		}
		record = next
	}
	data, _, err := openAnchorReleaseFile(filepath.Join(dir, "data.bin"), false)
	if err != nil {
		return record, err
	}
	defer data.Close()
	digest, err := hashAnchorRelease(ctx, data, nil, record.Size)
	if err != nil {
		return record, err
	}
	if digest != record.SHA256 {
		return record, errors.New("retained bytes do not match the release evidence")
	}
	return record, ctx.Err()
}

// This grants no authority to retry an effect. The future coordinator writes
// the REQUEST before each effect and must inspect the result on interruption.
// Immutable no-replace records arbitrate duplicate writers; no phase can be
// skipped or turned into "success". All copies remain, including failed writes.
func (j *anchorReleaseJournal) request(ctx context.Context, phase string) error {
	if err := j.validate(); err != nil {
		return err
	}
	dir := filepath.Join(j.store, j.id)
	guards, err := guardAnchorReleaseDirectories(dir, "")
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	record, err := j.inspect(ctx)
	if err != nil {
		return err
	}
	for i := 1; i < len(anchorReleasePhases); i++ {
		if phase == anchorReleasePhases[i] && record.Phase == anchorReleasePhases[i-1] {
			if err := ctx.Err(); err != nil {
				return err
			}
			record.Phase = phase
			return writeAnchorReleaseRecord(dir, i, record)
		}
	}
	return errors.New("release request is duplicate, out of order or unsupported")
}
