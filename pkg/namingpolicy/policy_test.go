package namingpolicy

import (
	"errors"
	"testing"
)

func TestTargetNameDerivesASCIIWithSingleExtension(t *testing.T) {
	got, err := TargetName(" rap.ort  Łódź  2026.PDF ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rap-ort-Lodz-2026.pdf" {
		t.Fatalf("got %q", got)
	}
}

func TestTargetNameRejectsPathAndMissingExtension(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{"", ErrEmpty},
		{"  ", ErrEmpty},
		{"a/b.pdf", ErrPath},
		{`a\b.pdf`, ErrPath},
		{"../x.pdf", ErrPath},
		{"readme", ErrNoExtension},
		{".env", ErrEmptyBase},
		{"readme.", ErrEmptyExt},
		{"file.tar.gz.exe!", ErrInvalidExt},
		{"....pdf", ErrPath},
	}
	for _, tc := range cases {
		_, err := TargetName(tc.in)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%q: err=%v want %v", tc.in, err, tc.want)
		}
	}
}

func TestTargetNameIsDeterministic(t *testing.T) {
	first, err := TargetName("Opinia-rzeczoznawcy.DOCX")
	if err != nil {
		t.Fatal(err)
	}
	second, err := TargetName("Opinia-rzeczoznawcy.DOCX")
	if err != nil || first != second || first != "Opinia-rzeczoznawcy.docx" {
		t.Fatalf("first=%q second=%q err=%v", first, second, err)
	}
}
