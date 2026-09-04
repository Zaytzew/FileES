package clientupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/internal/releaseenvelope"
	contract "filees/pkg/contract/v1"
)

// DirectoryInstaller replaces a set of files in one installation directory.
//
// It exists for the Windows desktop client, where the whole product is two
// executables and their autostart scripts sitting in
// %LOCALAPPDATA%\Programs\FileES - no prefix, no XDG split, no unit files. The
// Linux installer runs install-user.sh out of the bundle; this one does the
// work in Go instead, deliberately. A bundle that carries a script carries
// code, and a downloaded script is the one thing an update mechanism should not
// have to trust twice: the envelope is verified, and then the script runs
// anyway with whatever the machine's execution policy allows. Data in the
// bundle, logic in the signed binary.
//
// Nothing here is Windows-specific in the code, only in the reason. Rename
// exists everywhere, so this is tested on whatever platform the suite runs on
// rather than only where it ships.
type DirectoryInstaller struct {
	Stager BundleStager
	Paths  DirectoryPaths
	// now is injected by tests so a swap aside gets a predictable name.
	now func() time.Time
}

type DirectoryPaths struct {
	// InstallDir holds the executables and the autostart scripts.
	InstallDir string
	// ConfigPath is reported as preserved and is never written. It is the
	// owner's production configuration and an update has no business in it.
	ConfigPath string
}

// managedFile is one thing the bundle installs, with the word shown to a reader
// planning the update.
type managedFile struct {
	source string
	target string
	detail string
}

// windowsBundleFiles is the layout a windows-amd64 bundle must have.
//
// The names are the shipped ones rather than the build's: build-pair.sh emits
// filees.exe and filees-gui-wails.exe, and the bundle keeps those names so
// there is never a moment where the file being replaced is not the file being
// run.
func (installer DirectoryInstaller) managedFiles() []managedFile {
	dir := installer.Paths.InstallDir
	return []managedFile{
		{"bin/filees.exe", filepath.Join(dir, "filees.exe"), "demon"},
		{"bin/filees-gui-wails.exe", filepath.Join(dir, "filees-gui-wails.exe"), "interfejs"},
		{"autostart/start-filees.ps1", filepath.Join(dir, "start-filees.ps1"), "nadzorca autostartu"},
		{"autostart/start-filees.vbs", filepath.Join(dir, "start-filees.vbs"), "uruchamianie bez okna"},
	}
}

func (installer DirectoryInstaller) Plan(ctx context.Context, resolved *releaseenvelope.Resolved) ([]contract.UpdateChange, bool, error) {
	staged, err := installer.Stager.Stage(ctx, resolved)
	if err != nil {
		return nil, false, err
	}
	defer staged.Remove()
	paths, err := installer.normalizedPaths()
	if err != nil {
		return nil, false, err
	}
	if err := validateDirectoryBundle(staged.Root, installer.managedFiles()); err != nil {
		return nil, false, err
	}
	files := installer.managedFiles()
	changes := make([]contract.UpdateChange, 0, len(files)+1)
	for _, file := range files {
		action, err := compareFile(filepath.Join(staged.Root, filepath.FromSlash(file.source)), file.target)
		if err != nil {
			return nil, false, err
		}
		changes = append(changes, contract.UpdateChange{Action: action, Path: file.target, Detail: file.detail})
	}
	// Said out loud rather than left to inference. The owner has been burned by
	// tools that rewrite configuration during an upgrade, and a plan that
	// simply omits the file does not tell him it is safe.
	if paths.ConfigPath != "" {
		detail := "istniejąca konfiguracja zostanie zachowana"
		action := "unchanged"
		if _, err := os.Stat(paths.ConfigPath); errors.Is(err, os.ErrNotExist) {
			detail = "brak konfiguracji — aktualizacja jej nie tworzy"
		} else if err != nil {
			return nil, false, err
		}
		changes = append(changes, contract.UpdateChange{Action: action, Path: paths.ConfigPath, Detail: detail})
	}
	return changes, true, nil
}

func (installer DirectoryInstaller) Apply(ctx context.Context, resolved *releaseenvelope.Resolved) error {
	staged, err := installer.Stager.Stage(ctx, resolved)
	if err != nil {
		return err
	}
	defer staged.Remove()
	paths, err := installer.normalizedPaths()
	if err != nil {
		return err
	}
	files := installer.managedFiles()
	if err := validateDirectoryBundle(staged.Root, files); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.InstallDir, 0o755); err != nil {
		return err
	}
	// Everything is read and checked before anything is moved, so a bundle
	// that turns out to be short does not leave half a client on disk.
	payloads := make([][]byte, len(files))
	for i, file := range files {
		data, err := os.ReadFile(filepath.Join(staged.Root, filepath.FromSlash(file.source)))
		if err != nil {
			return fmt.Errorf("read %s from bundle: %w", file.source, err)
		}
		payloads[i] = data
	}
	stamp := installer.clock()().UTC().Format("20060102-150405")
	for i, file := range files {
		if err := installer.replace(file.target, payloads[i], stamp); err != nil {
			return fmt.Errorf("install %s: %w", file.target, err)
		}
	}
	installer.forgetSupersededFiles(paths.InstallDir)
	return nil
}

// replace puts content at target, moving whatever is there aside first.
//
// The move is the whole point. Windows refuses to overwrite or delete an
// executable that is running, and the daemon applying this update is running
// its own - but it does allow the running image to be renamed. So the old file
// is moved out of the way under a name nothing will launch, the new one takes
// its place, and the process keeps running from a file that now has a different
// name until it restarts.
//
// Written through a temporary file in the same directory and renamed into
// place, so an interrupted write cannot leave a truncated executable where a
// working one used to be.
func (installer DirectoryInstaller) replace(target string, content []byte, stamp string) error {
	directory := filepath.Dir(target)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(target)+".new-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		aside := target + supersededSuffix + stamp
		if err := os.Rename(target, aside); err != nil {
			return fmt.Errorf("move the running file aside: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempName, target)
}

// supersededSuffix marks a file moved aside by an update. It is deliberately
// not an extension Windows will launch or index as a program.
const supersededSuffix = ".superseded-"

// forgetSupersededFiles removes what earlier updates moved aside.
//
// Best effort and deliberately so: a superseded executable can only be deleted
// once nothing is running it, which for the daemon's own binary means after the
// restart this update requires. Failing to delete it is not a failed update -
// the next one will collect it - so nothing here is allowed to return an error.
func (installer DirectoryInstaller) forgetSupersededFiles(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), supersededSuffix) {
			continue
		}
		_ = os.Remove(filepath.Join(directory, entry.Name()))
	}
}

func (installer DirectoryInstaller) clock() func() time.Time {
	if installer.now != nil {
		return installer.now
	}
	return time.Now
}

func (installer DirectoryInstaller) normalizedPaths() (DirectoryPaths, error) {
	paths := installer.Paths
	paths.InstallDir = filepath.Clean(strings.TrimSpace(paths.InstallDir))
	if !filepath.IsAbs(paths.InstallDir) {
		return DirectoryPaths{}, errors.New("directory installer install_dir must be absolute")
	}
	if config := strings.TrimSpace(paths.ConfigPath); config != "" {
		paths.ConfigPath = filepath.Clean(config)
		if !filepath.IsAbs(paths.ConfigPath) {
			return DirectoryPaths{}, errors.New("directory installer config_path must be absolute")
		}
	} else {
		paths.ConfigPath = ""
	}
	return paths, nil
}

// validateDirectoryBundle refuses a bundle that is missing anything it claims
// to install, before a single file is moved.
//
// VERSION and SHA256SUMS are required for the same reason the Linux bundle
// requires them: they are what a human uses to tell one bundle from another
// after the fact, and a release that cannot be identified afterwards is not a
// release.
func validateDirectoryBundle(root string, files []managedFile) error {
	required := make([]string, 0, len(files)+2)
	required = append(required, "VERSION", "SHA256SUMS")
	for _, file := range files {
		required = append(required, file.source)
	}
	sort.Strings(required)
	for _, name := range required {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("bundle missing %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle entry %s is not a regular file", name)
		}
	}
	return nil
}

// compareFile reports what installing source over target would do. Shared by
// both installers: the question "is this file already what the bundle carries"
// has one answer regardless of platform.
func compareFile(source, target string) (string, error) {
	sourceDigest, err := localFileSHA256(source)
	if err != nil {
		return "", err
	}
	targetDigest, err := localFileSHA256(target)
	if errors.Is(err, os.ErrNotExist) {
		return "add", nil
	}
	if err != nil {
		return "", err
	}
	if sourceDigest == targetDigest {
		return "unchanged", nil
	}
	return "update", nil
}

func localFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// RequiredBundleFiles lists what a directory-installed bundle must contain,
// as slash-separated paths relative to its root.
//
// Exported so the thing that produces bundles can be checked against the thing
// that consumes them. The two live in different languages - a shell script
// assembles the layout, Go validates it - so nothing but a test can keep them
// agreeing, and the failure they would otherwise produce is invisible until a
// real update refuses a real release.
func RequiredBundleFiles() []string {
	installer := DirectoryInstaller{Paths: DirectoryPaths{InstallDir: string(filepath.Separator) + "unused"}}
	files := installer.managedFiles()
	required := make([]string, 0, len(files)+2)
	required = append(required, "VERSION", "SHA256SUMS")
	for _, file := range files {
		required = append(required, file.source)
	}
	sort.Strings(required)
	return required
}
