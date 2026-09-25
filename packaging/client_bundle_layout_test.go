//go:build !windows

package packaging_test

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"

	"filees/internal/clientupdate"
)

// The script that builds a client bundle and the code that installs one must
// agree on its layout, and nothing else can make them.
//
// The producer is a shell script and the consumer is Go, so a renamed file
// breaks the pair silently: the release is built, staged, signed and published,
// and the first sign of trouble is a client refusing an update it was given.
// That is a long way to travel for a typo.
//
// This checks names rather than behaviour on purpose. Actually building a
// bundle here would need a cross-compiler and several minutes; the failure
// worth catching is a path that stopped matching, and a path is a string.
func TestTheBundleBuilderStagesWhatTheInstallerRequires(t *testing.T) {
	raw, err := os.ReadFile("build-client-bundle.sh")
	if err != nil {
		t.Fatalf("the client bundle builder is missing: %v", err)
	}
	script := string(raw)

	for _, required := range clientupdate.RequiredBundleFiles() {
		// The script writes into $staging with the bundle-relative path, so the
		// path has to appear in it somewhere. SHA256SUMS is generated rather
		// than copied, and VERSION is written with printf, so both are matched
		// by name like the rest.
		if !strings.Contains(script, required) {
			t.Errorf("the bundle builder never stages %q, which the installer requires", required)
		}
	}
}

// Same check for the Linux producer in the same script, against
// clientupdate.RequiredLinuxBundleFiles instead of the Windows list.
func TestTheBundleBuilderStagesWhatTheLinuxInstallerRequires(t *testing.T) {
	raw, err := os.ReadFile("build-client-bundle.sh")
	if err != nil {
		t.Fatalf("the client bundle builder is missing: %v", err)
	}
	script := string(raw)

	for _, required := range clientupdate.RequiredLinuxBundleFiles() {
		if !strings.Contains(script, required) {
			t.Errorf("the bundle builder never stages %q, which the Linux installer requires", required)
		}
	}
}

// And the installer must not require something the script has no way to
// provide - a requirement nobody produces fails every release, which is the
// same fault pointing the other way.
func TestTheInstallerRequiresNothingTheBuilderCannotStage(t *testing.T) {
	required := clientupdate.RequiredBundleFiles()
	if len(required) == 0 {
		t.Fatal("the installer requires nothing at all; a bundle would be accepted empty")
	}
	for _, name := range required {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, `\`) {
			t.Errorf("required bundle path %q is not a plain relative path", name)
		}
	}
	// The two binaries are the product. A bundle that carried only scripts
	// would install cleanly and synchronise nothing.
	for _, essential := range []string{"bin/filees.exe", "bin/filees-gui-wails.exe"} {
		found := false
		for _, name := range required {
			if name == essential {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is not required, so a bundle without it would install", essential)
		}
	}
}

// A valid bundle is not enough: all MSI entry points must start the pair,
// including the shortcuts used after an intentional daemon shutdown. They go
// through filees-launch.exe, not wscript: a Windows without VBScript answered
// the old shortcut with "no script engine for .vbs" (2026-09-24).
func TestWindowsShortcutsStartTheSupervisedPair(t *testing.T) {
	raw, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Package struct {
			Components []struct {
				ID        string `xml:"Id,attr"`
				Shortcuts []struct {
					ID        string `xml:"Id,attr"`
					Target    string `xml:"Target,attr"`
					Arguments string `xml:"Arguments,attr"`
				} `xml:"Shortcut"`
			} `xml:"Component"`
		} `xml:"Package"`
	}
	if err := xml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"FileESStartMenuShortcut": "--show",
		"FileESDesktopShortcut":   "--show",
		"FileESStartupShortcut":   "",
	}
	for _, component := range document.Package.Components {
		for _, shortcut := range component.Shortcuts {
			arguments, ok := want[shortcut.ID]
			if !ok {
				continue
			}
			if component.ID != "FileESLauncher" || shortcut.Target != "[INSTALLFOLDER]filees-launch.exe" || shortcut.Arguments != arguments {
				t.Fatalf("shortcut bypasses supervisor: %+v in %s", shortcut, component.ID)
			}
			delete(want, shortcut.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing supervised shortcuts: %v", want)
	}
}

// 2026-09-25: the r1558 MSI registered the new version over a self-updated
// pair, kept the old binaries (unversioned, "modified since install") and
// stopped on the daemon's open log. Every install now rewrites all files,
// stops the pair before the files-in-use check and starts it afterwards.
func TestWindowsMSIReplacesSelfUpdatedFilesAndRestartsThePair(t *testing.T) {
	raw, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Package struct {
			Properties []struct {
				ID    string `xml:"Id,attr"`
				Value string `xml:"Value,attr"`
			} `xml:"Property"`
			Actions []struct {
				ID      string `xml:"Id,attr"`
				Command string `xml:"ExeCommand,attr"`
				Return  string `xml:"Return,attr"`
			} `xml:"CustomAction"`
		} `xml:"Package"`
	}
	if err := xml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	reinstall := ""
	for _, property := range document.Package.Properties {
		if property.ID == "REINSTALLMODE" {
			reinstall = property.Value
		}
	}
	if !strings.Contains(reinstall, "a") {
		t.Fatalf("REINSTALLMODE=%q does not force every file", reinstall)
	}
	commands := map[string]string{}
	for _, action := range document.Package.Actions {
		commands[action.ID] = action.Command + " return=" + action.Return
	}
	if commands["StopFileESPair"] != `"[INSTALLFOLDER]filees.exe" shutdown return=ignore` {
		t.Fatalf("stop action: %q", commands["StopFileESPair"])
	}
	if commands["StartFileESPair"] != `"[INSTALLFOLDER]filees-launch.exe" return=asyncNoWait` {
		t.Fatalf("start action: %q", commands["StartFileESPair"])
	}
	if !strings.Contains(string(raw), `<Custom Action="StopFileESPair" Before="InstallValidate"`) || !strings.Contains(string(raw), `<Custom Action="StartFileESPair" After="InstallFinalize"`) {
		t.Fatal("pair actions are not scheduled around the file replacement")
	}
}
