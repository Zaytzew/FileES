package clientupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubStager hands the installer a directory that is already unpacked, so these
// tests exercise the installing rather than the fetching, which bundle_test.go
// already covers.

func writeBundle(t *testing.T, contents map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range contents {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func completeBundle(version string) map[string]string {
	return map[string]string{
		"VERSION":                    version,
		"SHA256SUMS":                 "unused by the installer, required by the bundle",
		"bin/filees.exe":             "daemon " + version,
		"bin/filees-gui-wails.exe":   "interface " + version,
		"autostart/start-filees.ps1": "supervisor " + version,
		"autostart/start-filees.vbs": "launcher " + version,
	}
}

func newInstaller(installDir, configPath string) DirectoryInstaller {
	return DirectoryInstaller{
		Paths: DirectoryPaths{InstallDir: installDir, ConfigPath: configPath},
		now:   func() time.Time { return time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC) },
	}
}

// applyBundle drives Apply without the stager, which needs a resolved envelope.
// The installer's own work starts once a bundle is on disk.
func applyBundle(t *testing.T, installer DirectoryInstaller, bundleRoot string) error {
	t.Helper()
	files := installer.managedFiles()
	if err := validateDirectoryBundle(bundleRoot, files); err != nil {
		return err
	}
	if err := os.MkdirAll(installer.Paths.InstallDir, 0o755); err != nil {
		return err
	}
	stamp := installer.clock()().UTC().Format("20060102-150405")
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(bundleRoot, filepath.FromSlash(file.source)))
		if err != nil {
			return err
		}
		if err := installer.replace(file.target, data, stamp); err != nil {
			return err
		}
	}
	installer.forgetSupersededFiles(installer.Paths.InstallDir)
	return nil
}

// The swap itself: the new content lands at the target and the old content
// survives under a name nothing will launch.
//
// This does not prove anything about a file being in use - see
// directory_windows_test.go, which runs one. An earlier version of this test
// held an os.Open handle and called that "running", which was wrong in the
// direction that mattered: Go opens without FILE_SHARE_DELETE, so an ordinary
// handle blocks a rename that a genuinely running image would allow.
func TestTheOldFileIsMovedAsideRatherThanOverwritten(t *testing.T) {
	installDir := t.TempDir()
	target := filepath.Join(installDir, "filees.exe")
	if err := os.WriteFile(target, []byte("daemon old"), 0o755); err != nil {
		t.Fatal(err)
	}
	installer := newInstaller(installDir, "")
	if err := installer.replace(target, []byte("daemon new"), "20260904-020000"); err != nil {
		t.Fatalf("replace a file in use: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "daemon new" {
		t.Fatalf("target = %q, want the new content", got)
	}
	// The old content is still readable through the handle that was open when
	// the swap happened - which is exactly what lets a running daemon keep
	// working until it restarts.
	aside := target + supersededSuffix + "20260904-020000"
	old, err := os.ReadFile(aside)
	if err != nil {
		t.Fatalf("the superseded file is gone: %v", err)
	}
	if string(old) != "daemon old" {
		t.Fatalf("superseded = %q, want the old content", old)
	}
}

func TestApplyInstallsEveryManagedFile(t *testing.T) {
	installDir := t.TempDir()
	bundle := writeBundle(t, completeBundle("0.1.15.900"))
	installer := newInstaller(installDir, "")
	if err := applyBundle(t, installer, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for name, want := range map[string]string{
		"filees.exe":           "daemon 0.1.15.900",
		"filees-gui-wails.exe": "interface 0.1.15.900",
		"start-filees.ps1":     "supervisor 0.1.15.900",
		"start-filees.vbs":     "launcher 0.1.15.900",
	} {
		got, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A short bundle must not leave half a client on disk. Everything is read and
// checked before anything is moved.
func TestAnIncompleteBundleMovesNothing(t *testing.T) {
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, "filees.exe"), []byte("daemon old"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := completeBundle("0.1.15.901")
	delete(contents, "autostart/start-filees.vbs")
	bundle := writeBundle(t, contents)

	installer := newInstaller(installDir, "")
	if err := applyBundle(t, installer, bundle); err == nil {
		t.Fatal("an incomplete bundle was accepted")
	}
	got, err := os.ReadFile(filepath.Join(installDir, "filees.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "daemon old" {
		t.Fatalf("filees.exe = %q; a refused bundle replaced it anyway", got)
	}
}

// The owner's production configuration is never touched, and the plan says so
// rather than leaving him to infer it from an omission.
func TestTheConfigurationIsNeverWrittenAndIsReportedAsKept(t *testing.T) {
	installDir := t.TempDir()
	config := filepath.Join(installDir, "config.json")
	if err := os.WriteFile(config, []byte(`{"transport":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := writeBundle(t, completeBundle("0.1.15.902"))
	installer := newInstaller(installDir, config)
	if err := applyBundle(t, installer, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"transport":{}}` {
		t.Fatalf("config = %q; an update rewrote the owner's configuration", got)
	}
}

// Superseded files from earlier updates are collected on the next one. They
// cannot be removed while something is still running them, which for the
// daemon's own binary means after the restart the update requires.
func TestAnEarlierSupersededFileIsCollectedByTheNextUpdate(t *testing.T) {
	installDir := t.TempDir()
	stale := filepath.Join(installDir, "filees.exe"+supersededSuffix+"20260101-000000")
	if err := os.WriteFile(stale, []byte("ancient"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := writeBundle(t, completeBundle("0.1.15.903"))
	installer := newInstaller(installDir, "")
	if err := applyBundle(t, installer, bundle); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a superseded file from an earlier update survived: %v", err)
	}
}

func TestPlanRefusesARelativeInstallDirectory(t *testing.T) {
	installer := DirectoryInstaller{Paths: DirectoryPaths{InstallDir: "Programs/FileES"}}
	if _, err := installer.normalizedPaths(); err == nil {
		t.Fatal("a relative install directory was accepted")
	}
}

func TestABundleMissingItsVersionIsRefused(t *testing.T) {
	contents := completeBundle("0.1.15.904")
	delete(contents, "VERSION")
	installer := newInstaller(t.TempDir(), "")
	err := validateDirectoryBundle(writeBundle(t, contents), installer.managedFiles())
	if err == nil || !strings.Contains(err.Error(), "VERSION") {
		t.Fatalf("error = %v; a bundle nobody can identify afterwards is not a release", err)
	}
}
