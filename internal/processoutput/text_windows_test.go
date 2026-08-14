//go:build windows

package processoutput

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestTextDecodesActiveWindowsANSIPage(t *testing.T) {
	if codePage := windows.GetACP(); codePage != 1250 {
		t.Skipf("Polish CP1250 fixture does not apply to active Windows code page %d", codePage)
	}
	encoded := []byte{0xa3, 0xd3, 'D', 0x8f, '-', 'k', 'o', 'l', 'u', 'm', 'n', 'y'}
	if got, want := Text(encoded), "ŁÓDŹ-kolumny"; got != want {
		t.Fatalf("Text(CP1250) = %q, want %q", got, want)
	}
}
