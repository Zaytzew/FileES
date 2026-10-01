// filees-native-package is a build-time tool, not a client update installer.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"filees/internal/nativeruntime"
)

func main() {
	args := os.Args[1:]
	files := len(args) == 4 && args[0] == "--files"
	if files {
		args = args[1:]
	}
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: filees-native-package [--files] SOURCE-TREE STAGED-RUNTIME OUTPUT-DIRECTORY")
		os.Exit(2)
	}
	var err error
	if files {
		err = runFiles(args[0], args[1], args[2])
	} else {
		err = run(args[0], args[1], args[2])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runFiles is build-time only. Unlike the embedded producer it copies the
// measured helper/DLL/notices as ordinary installation files, plus an inventory.
func runFiles(source, stage, output string) error {
	if err := verifySources(source, stage); err != nil {
		return err
	}
	payload, err := nativeruntime.Pack(stage)
	if err != nil {
		return err
	}
	raw, files, err := nativeruntime.BuildFileManifest(payload)
	if err != nil {
		return err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	base := nativeruntime.FilesRoot(output, raw)
	for name, data := range files {
		path := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(output, "native-svn.json"), raw, 0644); err != nil {
		return err
	}
	_, err = nativeruntime.VerifyFiles(base, raw)
	if err == nil {
		fmt.Println(nativeruntime.ID(raw))
	}
	return err
}

func run(source, stage, output string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := verifySources(source, stage); err != nil {
		return err
	}
	payload, err := nativeruntime.Pack(stage)
	if err != nil {
		return err
	}
	// An existing directory is an error, never a recursive-delete target.
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	path := filepath.Join(output, "runtime.payload")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		return err
	}
	overlay := struct{ Replace map[string]string }{map[string]string{
		filepath.Join(source, "internal", "nativeruntime", "runtime.payload"): path,
	}}
	raw, err := json.Marshal(overlay)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "overlay.json"), raw, 0600); err != nil {
		return err
	}
	fmt.Println(nativeruntime.ID(payload))
	return nil
}

// An exported FILEES_NATIVE_RUNTIME may outlive a C edit. Refuse that stale
// stage before building a new daemon that would otherwise claim this revision.
func verifySources(source, stage string) error {
	raw, err := os.ReadFile(filepath.Join(stage, "notices", "DEPENDENCIES.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Sources []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	want := make(map[string]string)
	for _, item := range manifest.Sources {
		if _, ok := want[item.Name]; ok {
			return fmt.Errorf("duplicate runtime source %s", item.Name)
		}
		want[item.Name] = item.SHA256
	}
	native := filepath.Join(source, "native", "filees-svn")
	var names = []string{"CMakeLists.txt"}
	if err := filepath.WalkDir(filepath.Join(native, "src"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".c" && ext != ".h" {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular runtime source %s", path)
		}
		rel, err := filepath.Rel(native, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return err
	}
	if len(names) != len(want) {
		return fmt.Errorf("native runtime source set changed; rebuild the stage")
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(native, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if nativeruntime.ID(data) != want[name] {
			return fmt.Errorf("native runtime source changed: %s; rebuild the stage", name)
		}
	}
	return nil
}
