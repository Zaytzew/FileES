package nativeruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileManifest is the non-executable inventory compiled into a sidecar build.
// The files arrive through MSI/the signed bundle, never through daemon startup.
type FileManifest struct {
	Schema   string       `json:"schema"`
	Platform string       `json:"platform"`
	Files    []FileDigest `json:"files"`
}
type FileDigest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func BuildFileManifest(payload []byte) ([]byte, map[string][]byte, error) {
	files, err := unpack(payload)
	if err != nil {
		return nil, nil, err
	}
	if len(files[Executable]) == 0 {
		return nil, nil, errors.New("sidecar layout currently requires Windows runtime")
	}
	m := FileManifest{Schema: "filees.native-files/v1", Platform: "windows-amd64"}
	for name, data := range files {
		m.Files = append(m.Files, FileDigest{name, ID(data), int64(len(data))})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	raw, err := json.Marshal(m)
	return raw, files, err
}

func ParseFileManifest(raw []byte) (*FileManifest, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("native file manifest too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var m FileManifest
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing native file manifest")
	}
	if m.Schema != "filees.native-files/v1" || m.Platform != "windows-amd64" || len(m.Files) < 2 || len(m.Files) > 128 {
		return nil, errors.New("invalid native file manifest identity")
	}
	seen := map[string]bool{}
	var total int64
	previous := ""
	for _, f := range m.Files {
		fold := strings.ToLower(f.Name)
		if !validName(f.Name) || f.Name <= previous || seen[fold] || !runtimeID(f.SHA256) || f.Size <= 0 || f.Size > maxPayload-total {
			return nil, fmt.Errorf("invalid native file %q", f.Name)
		}
		if f.Name == LinuxExecutable || linuxLibrary.MatchString(f.Name) {
			return nil, errors.New("Linux file in Windows runtime")
		}
		seen[fold] = true
		previous = f.Name
		total += f.Size
	}
	for _, name := range append([]string{Executable, "notices/DEPENDENCIES.json"}, noticePaths()...) {
		if !seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("missing native file %s", name)
		}
	}
	return &m, nil
}
func noticePaths() []string {
	out := make([]string, 0, len(requiredNotices))
	for _, n := range requiredNotices {
		out = append(out, "notices/"+n)
	}
	return out
}

// VerifyFiles is read-only, refuses extra files, symlinks/reparse paths and
// byte mismatches. No repair or cache fallback is allowed for a sidecar build.
func VerifyFiles(root string, raw []byte) (string, error) {
	m, err := ParseFileManifest(raw)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("native directory must be absolute")
	}
	if err := plainPath(root); err != nil {
		return "", err
	}
	expected := map[string]FileDigest{}
	for _, f := range m.Files {
		expected[f.Name] = f
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		name := filepath.ToSlash(rel)
		if name == "." {
			return nil
		}
		if d.IsDir() && name == "notices" {
			return plainPath(path)
		}
		item, ok := expected[name]
		if !ok || !d.Type().IsRegular() {
			return fmt.Errorf("unexpected native file %s", name)
		}
		if err := plainPath(filepath.Dir(path)); err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Size() != item.Size {
			return fmt.Errorf("native size mismatch: %s", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ID(data) != item.SHA256 {
			return fmt.Errorf("native hash mismatch: %s", name)
		}
		delete(expected, name)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(expected) != 0 {
		return "", errors.New("native runtime files are missing")
	}
	return filepath.Join(root, Executable), nil
}

// FilesRoot binds immutable installation files to the exact inventory bytes.
func FilesRoot(base string, raw []byte) string { return filepath.Join(base, "native-svn", ID(raw)) }

// PublishFiles is installer-only. Publish a complete immutable directory
// before any executable swap; leave older directories for processes/rollback.
func PublishFiles(source, base string, raw []byte) error {
	if _, err := VerifyFiles(source, raw); err != nil {
		return err
	}
	target := FilesRoot(base, raw)
	if err := plainPath(target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		_, err = VerifyFiles(target, raw)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) // fresh owned child only
	m, err := ParseFileManifest(raw)
	if err != nil {
		return err
	}
	for _, item := range m.Files {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(item.Name)))
		if err != nil {
			return err
		}
		if int64(len(data)) != item.Size || ID(data) != item.SHA256 {
			return errors.New("native source changed during install")
		}
		path := filepath.Join(stage, filepath.FromSlash(item.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return err
		}
	}
	if _, err := VerifyFiles(stage, raw); err != nil {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		if _, check := VerifyFiles(target, raw); check != nil {
			return fmt.Errorf("publish native files: %w (%v)", err, check)
		}
	}
	_, err = VerifyFiles(target, raw)
	return err
}
