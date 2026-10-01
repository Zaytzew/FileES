package clientupdate

import (
	"errors"
	"filees/internal/nativeruntime"
	"os"
	"path/filepath"
)

func nativeBundleFiles(root, install string) ([]managedFile, error) {
	raw, err := os.ReadFile(filepath.Join(root, "bin", "native-svn.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := nativeruntime.ParseFileManifest(raw)
	if err != nil {
		return nil, err
	}
	source := nativeruntime.FilesRoot(filepath.Join(root, "bin"), raw)
	if _, err := nativeruntime.VerifyFiles(source, raw); err != nil {
		return nil, err
	}
	target := nativeruntime.FilesRoot(install, raw)
	files := make([]managedFile, 0, len(m.Files))
	for _, f := range m.Files {
		path := filepath.Join(source, filepath.FromSlash(f.Name))
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		files = append(files, managedFile{source: filepath.ToSlash(rel), target: filepath.Join(target, filepath.FromSlash(f.Name)), detail: "natywny runtime SVN"})
	}
	return files, nil
}
