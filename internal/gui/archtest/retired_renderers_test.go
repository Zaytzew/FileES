package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredRenderersCannotReturn(t *testing.T) {
	root := moduleRoot(t)
	for _, path := range []string{"cmd/filees-gui/main.go", "internal/gui/tray/systray_backend.go", "packaging/build-gui.sh"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !os.IsNotExist(err) {
			t.Errorf("retired renderer path must stay absent: %s (%v)", path, err)
		}
	}
	for _, path := range []string{"internal/gui/platform/linux.go", "internal/gui/platform/windows.go", "go.mod"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"fyne.io/", `LookPath("zenity")`, `LookPath("yad")`, `LookPath("kdialog")`, "System.Windows.Forms"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s reintroduces retired renderer %q", path, forbidden)
			}
		}
	}
}
