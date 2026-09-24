package packaging_test

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// The owner's decisions of 2026-09-24, after a clean Windows showed what the
// owner's machine hid. Both live in build inputs no Go code executes, so only
// a test keeps them from quietly disappearing.

// The Microsoft Store package ships without Explorer anchors and without any
// Cloud Files API code: the daemon is built with nocfapi, and the script
// refuses a binary that still carries it.
func TestStorePackageIsBuiltWithoutCloudFiles(t *testing.T) {
	raw, err := os.ReadFile("windows/build-store-msix.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, "-tags native_svn_bundle,nocfapi") {
		t.Fatal("the Store daemon is not built with the nocfapi tag")
	}
	for _, marker := range []string{"'cldapi'", "'CfGetPlaceholderState'", "'filees-cfapi.exe'"} {
		if !strings.Contains(script, marker) {
			t.Fatalf("the Store build no longer checks the daemon for %s", marker)
		}
	}
}

// FileES reaches every server through the Windows OpenSSH client, so the MSI
// refuses to install without it - never blocking repair or uninstall.
func TestMSIRequiresTheOpenSSHClient(t *testing.T) {
	raw, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Package struct {
			Properties []struct {
				ID     string `xml:"Id,attr"`
				Search struct {
					Path string `xml:"Path,attr"`
					File struct {
						Name string `xml:"Name,attr"`
					} `xml:"FileSearch"`
				} `xml:"DirectorySearch"`
			} `xml:"Property"`
			Launch []struct {
				Condition string `xml:"Condition,attr"`
				Message   string `xml:"Message,attr"`
			} `xml:"Launch"`
		} `xml:"Package"`
	}
	if err := xml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	searches := map[string]string{}
	for _, property := range document.Package.Properties {
		if property.Search.File.Name == "ssh.exe" {
			searches[property.ID] = property.Search.Path
		}
	}
	if searches["FILEESOPENSSHSYSTEM"] != "[System64Folder]OpenSSH" || searches["FILEESOPENSSHPROGRAMS"] != "[ProgramFiles64Folder]OpenSSH" {
		t.Fatalf("ssh.exe searches = %v", searches)
	}
	for _, launch := range document.Package.Launch {
		if launch.Condition == "Installed OR FILEESOPENSSHSYSTEM OR FILEESOPENSSHPROGRAMS" {
			if !strings.Contains(launch.Message, "OpenSSH") {
				t.Fatalf("the refusal does not say what to install: %q", launch.Message)
			}
			return
		}
	}
	t.Fatal("the MSI installs without checking for the OpenSSH client")
}
