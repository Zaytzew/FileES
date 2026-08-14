package processoutput

import "testing"

func TestTextPreservesUTF8(t *testing.T) {
	const want = "ŁÓDŹ-kolumny — zażółć"
	if got := Text([]byte(want)); got != want {
		t.Fatalf("Text() = %q, want %q", got, want)
	}
}

func TestUTF8RejectsLegacyCodePageBytes(t *testing.T) {
	if got, err := UTF8([]byte{0xa3, 0xd3, 'D', 0x8f}); err == nil || got != "" {
		t.Fatalf("UTF8() = %q, %v; want rejection", got, err)
	}
}
