package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversionBoundCheckoutPinsNumericRevisionAndRefusesLegacy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-helper", true: "bound"}[enabled], func(t *testing.T) {
			c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":7,"conflicts":[],"changes":[]}`)
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("FILEES_TEST_RA_TRACE", trace)
			if enabled {
				t.Setenv("FILEES_TEST_CONVERSION_BOUND", "1")
			}
			_, err := c.CheckoutDepthEmptyAt(t.Context(), "file:///fixture", t.TempDir(), 7)
			if !enabled {
				if err == nil {
					t.Fatal("legacy helper accepted")
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatal("mutation attempted before capability")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"--revision","7"`) || !strings.Contains(string(raw), `"--depth","empty"`) || strings.Contains(string(raw), `"update"`) {
				t.Fatal("unbound/full checkout", string(raw))
			}
			if _, err := c.CheckoutDepthEmptyAt(t.Context(), "file:///fixture", t.TempDir(), -1); err == nil {
				t.Fatal("negative revision accepted")
			}
			wc := t.TempDir()
			if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CheckoutDepthEmptyAt(t.Context(), "file:///fixture", wc, 7); err == nil {
				t.Fatal("existing WC replayed")
			}
		})
	}
}

func TestConversionRootIdentityRequiresExplicitUUIDAndDirectory(t *testing.T) {
	for _, kind := range []string{"dir", "file"} {
		t.Run(kind, func(t *testing.T) {
			c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
			t.Setenv("FILEES_TEST_CONVERSION_BOUND", "1")
			t.Setenv("FILEES_TEST_CONVERSION_INFO", `{"schema":"filees.native-svn/v1","ok":true,"entries":[{"path":".","url":"file:///lab","repos_root_url":"file:///lab","repos_uuid":"a72d443d-342b-4ed8-9412-925247dbd4c5","kind":"`+kind+`","revision":0,"last_changed_rev":0}]}`)
			info, err := c.InspectConversionRoot(t.Context(), "file:///lab")
			if kind == "dir" {
				if err != nil || info.Revision != 0 || info.UUID == "" {
					t.Fatal(info, err)
				}
			} else if err == nil {
				t.Fatal("non-directory identity accepted")
			}
		})
	}
}
