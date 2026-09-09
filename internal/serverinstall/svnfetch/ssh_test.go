package svnfetch

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHFetchRefusesBeforeAnyProgramWithoutPins(t *testing.T) {
	for _, native := range []bool{false, true} {
		fetch := SVN{Program: filepath.Join(t.TempDir(), "absent-cli"), RepoURL: "svn+ssh://release@host/repo"}
		if native {
			fetch.NativeProgram = filepath.Join(t.TempDir(), "absent-helper")
		}
		_, err := fetch.Cat(t.Context(), "manifest.json")
		if err == nil || !strings.Contains(err.Error(), "pinned") {
			t.Fatalf("native=%v: %v", native, err)
		}
	}
}
