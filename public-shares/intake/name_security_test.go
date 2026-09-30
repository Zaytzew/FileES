package intake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type unreadUploadBody struct{ t *testing.T }

func (b unreadUploadBody) Read([]byte) (int, error) {
	b.t.Error("invalid filename reached the payload reader")
	return 0, errors.New("unexpected read")
}

func TestAcceptRejectsControlNamesBeforeReadingOrWriting(t *testing.T) {
	controls := []rune{'\u061c', '\u200e', '\u200f', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', '\u2066', '\u2067', '\u2068', '\u2069'}
	for r := rune(0); r <= 0x9f; r++ {
		if r < 0x20 || r >= 0x7f {
			controls = append(controls, r)
		}
	}
	for _, r := range controls {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			store := Store{Root: filepath.Join(t.TempDir(), "intake"), MaxBytes: 1024}
			for _, name := range []string{string(r) + "report.pdf", "report" + string(r) + ".pdf", "report.pdf" + string(r)} {
				_, err := store.Accept(uuid.NewString(), "realm", "shelf", strings.Repeat("a", 64), name, unreadUploadBody{t})
				if !errors.Is(err, ErrName) {
					t.Fatalf("name %q: got %v, want ErrName", name, err)
				}
				if _, err := os.Stat(store.Root); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid name created intake state: %v", err)
				}
			}
		})
	}
}

func TestOriginalNamePreservesInternationalText(t *testing.T) {
	for _, name := range []string{"Opinia Łódź.pdf", "تقرير.pdf", "דוח.pdf", "報告.pdf", "emoji-👩‍💻.png", "--help.txt"} {
		if got, err := boundedOriginalName(name); err != nil || got != name {
			t.Errorf("%q: got %q, %v", name, got, err)
		}
	}
}
