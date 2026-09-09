// Package nativeruntime installs the immutable native SVN payload embedded in
// a Windows daemon. It does not download code or select the newest runtime.
package nativeruntime

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const Executable = "filees-svn.exe"
const maxPayload = 256 << 20

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var deviceName = regexp.MustCompile(`^(COM|LPT)[0-9]$`)

var requiredNotices = []string{
	"FileES-LICENSE.txt", "Subversion-LICENSE.txt", "Subversion-NOTICE.txt",
	"MSVC-Redist.txt", "RUNTIME-NOTICE.txt", "apr-copyright.txt", "apr-util-copyright.txt",
	"expat-copyright.txt", "openssl-copyright.txt", "serf-copyright.txt",
	"sqlite3-copyright.txt", "zlib-copyright.txt",
}

func validName(name string) bool {
	parts := strings.Split(name, "/")
	leaf := parts[len(parts)-1]
	if strings.HasSuffix(leaf, ".") {
		return false
	}
	base := strings.ToUpper(strings.SplitN(leaf, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || deviceName.MatchString(base) {
		return false
	}
	if len(parts) == 2 {
		return parts[0] == "notices" && safeName.MatchString(parts[1]) && !strings.Contains(parts[1], "..")
	}
	return len(parts) == 1 && safeName.MatchString(name) && !strings.Contains(name, "..") &&
		(name == Executable || strings.HasSuffix(name, ".dll"))
}

// Pack creates a deterministic archive from an explicitly assembled runtime.
// PE dependency resolution belongs to the Windows packager, not the daemon.
func Pack(root string) ([]byte, error) {
	if err := plainPath(root); err != nil {
		return nil, err
	}
	var names []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() && name == "notices" {
			return nil
		}
		if !d.Type().IsRegular() || !validName(name) {
			return fmt.Errorf("invalid runtime entry %q", name)
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	var total int64
	for _, name := range names {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		dst, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			f.Close()
			return nil, err
		}
		n, err := io.Copy(dst, io.LimitReader(f, maxPayload-total+1))
		f.Close()
		total += n
		if err != nil {
			return nil, err
		}
		if total > maxPayload {
			return nil, errors.New("native runtime exceeds size limit")
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if _, err := unpack(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ID is content-addressed, including notices. It is not a version selector or
// a substitute for the release envelope's signature.
func ID(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

func unpack(payload []byte) (map[string][]byte, error) {
	if len(payload) > maxPayload {
		return nil, errors.New("native runtime archive too large")
	}
	r, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("native runtime archive: %w", err)
	}
	if len(r.File) == 0 || len(r.File) > 128 {
		return nil, errors.New("invalid native runtime entry count")
	}
	files := make(map[string][]byte, len(r.File))
	seen := make(map[string]bool, len(r.File))
	total := 0
	for _, f := range r.File {
		fold := strings.ToLower(f.Name)
		if !validName(f.Name) || seen[fold] || !f.Mode().IsRegular() {
			return nil, fmt.Errorf("invalid or duplicate native runtime entry %q", f.Name)
		}
		seen[fold] = true
		if f.UncompressedSize64 == 0 || f.UncompressedSize64 > uint64(maxPayload-total) {
			return nil, fmt.Errorf("invalid native runtime size: %s", f.Name)
		}
		src, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(src, int64(maxPayload-total)+1))
		src.Close()
		if err != nil {
			return nil, err
		} // includes ZIP CRC failure
		total += len(data)
		if total > maxPayload {
			return nil, errors.New("native runtime exceeds size limit")
		}
		files[f.Name] = data
	}
	for _, name := range []string{Executable, "notices/FileES-LICENSE.txt", "notices/DEPENDENCIES.json"} {
		if len(files[name]) == 0 {
			return nil, fmt.Errorf("native runtime missing %s", name)
		}
	}
	if err := checkInventory(files); err != nil {
		return nil, err
	}
	for _, name := range requiredNotices {
		if len(files["notices/"+name]) == 0 {
			return nil, fmt.Errorf("native runtime missing notice %s", name)
		}
	}
	return files, nil
}

type inventory struct {
	Schema        string          `json:"schema"`
	Platform      string          `json:"platform"`
	Files         []inventoryFile `json:"files"`
	Sources       []inventoryFile `json:"sources"`
	SystemImports []string        `json:"system_imports"`
}

type inventoryFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// The build's measured dependency list must match all PE files byte for byte.
// This prevents an accidentally shortened stage from becoming a valid bundle.
// The list is not an independent trust root: it is inside the signed daemon.
func checkInventory(files map[string][]byte) error {
	var list inventory
	d := json.NewDecoder(bytes.NewReader(files["notices/DEPENDENCIES.json"]))
	d.DisallowUnknownFields()
	if err := d.Decode(&list); err != nil {
		return fmt.Errorf("native runtime inventory: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing native runtime inventory data")
	}
	if list.Schema != "filees.native-runtime/v1" || list.Platform != "windows-amd64" || len(list.Files) < 2 {
		return errors.New("invalid native runtime inventory identity")
	}
	seen := make(map[string]bool)
	for _, item := range list.Files {
		data, ok := files[item.Name]
		if !ok || strings.Contains(item.Name, "/") || seen[item.Name] || ID(data) != item.SHA256 {
			return fmt.Errorf("native runtime inventory mismatch: %s", item.Name)
		}
		seen[item.Name] = true
	}
	for name := range files {
		if !strings.Contains(name, "/") && !seen[name] {
			return fmt.Errorf("native runtime inventory missing %s", name)
		}
	}
	return nil
}

// Ensure stages all files before publishing a runtime directory. Existing
// runtimes are verified, never repaired in place: an older process may still
// be using them, even when no helper process is currently visible.
func Ensure(cache string, payload []byte) (string, error) {
	files, err := unpack(payload)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(cache) {
		return "", errors.New("native runtime cache must be absolute")
	}
	if err := plainPath(cache); err != nil {
		return "", err
	}
	root := filepath.Join(cache, ID(payload))
	if _, err := os.Lstat(root); err == nil {
		if err := verify(root, files); err != nil {
			return "", err
		}
		return filepath.Join(root, Executable), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", err
	}
	if err := plainPath(cache); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(cache, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage) // owned, fresh child; never remove a published runtime
	for name, data := range files {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return "", err
		}
	}
	if err := os.Rename(stage, root); err != nil {
		// Another process may have published this exact payload first. Verify
		// every byte, including refusal of extra DLLs; existence is not proof.
		if check := verify(root, files); check != nil {
			return "", fmt.Errorf("publish native runtime: %w (%v)", err, check)
		}
	}
	if err := verify(root, files); err != nil {
		return "", err
	}
	return filepath.Join(root, Executable), nil
}

func plainPath(path string) error {
	for {
		st, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!st.IsDir() || st.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0) {
			return fmt.Errorf("native runtime requires a plain directory: %s", path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func verify(root string, files map[string][]byte) error {
	if err := plainPath(root); err != nil {
		return err
	}
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() && name == "notices" {
			return nil
		}
		want, ok := files[name]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("unexpected native runtime entry %s", path)
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Size() != int64(len(want)) {
			return fmt.Errorf("native runtime size mismatch: %s", path)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("native runtime content mismatch: %s", path)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(files) {
		return errors.New("native runtime is incomplete")
	}
	return nil
}
