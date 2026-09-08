package portablepath

import "testing"

func TestRepresentableNamesArePassed(t *testing.T) {
	for _, name := range []string{
		"rysunek.dwg",
		"Umowa 2026-09.pdf",
		"Ładne polskie znaki ąćęłńóśźż.txt",
		"con-tener.txt", // only a prefix of a device name
		"plik.con",      // device name in the extension, not the stem
		".ukryty",       // a leading dot is ordinary
		"kropka.w.srodku",
	} {
		if p := SegmentProblem(name); p != nil {
			t.Errorf("SegmentProblem(%q) = %v, want representable", name, p)
		}
	}
}

func TestReservedDeviceNames(t *testing.T) {
	// Reserved with or without an extension and regardless of case: all three
	// of these resolve to the device, not to a file.
	for _, name := range []string{"CON", "con", "con.txt", "LPT1", "lpt9.dwg", "NUL"} {
		p := SegmentProblem(name)
		if p == nil || p.Kind != ReservedDevice {
			t.Errorf("SegmentProblem(%q) = %v, want ReservedDevice", name, p)
		}
	}
}

func TestReservedRunes(t *testing.T) {
	for _, name := range []string{"a:b.txt", "co?.txt", `cudzy"slow.txt`, "a|b", "a<b", "a>b", "gwiazda*.txt"} {
		p := SegmentProblem(name)
		if p == nil || p.Kind != ReservedRune {
			t.Errorf("SegmentProblem(%q) = %v, want ReservedRune", name, p)
		}
	}
}

// A separator inside a single name is its own mistake. A Linux file genuinely
// named "a\b.txt" is one name there and two path components on Windows.
func TestSeparatorInsideASegment(t *testing.T) {
	for _, name := range []string{`a\b.txt`, "a/b.txt"} {
		p := SegmentProblem(name)
		if p == nil || p.Kind != Separator {
			t.Errorf("SegmentProblem(%q) = %v, want Separator", name, p)
		}
	}
}

// Windows strips a trailing dot or space, so the file that appears is not the
// one that was asked for - the name silently becomes a different name.
func TestTrailingDotOrSpace(t *testing.T) {
	for _, name := range []string{"plik.txt.", "plik.txt ", "nazwa."} {
		p := SegmentProblem(name)
		if p == nil || p.Kind != TrailingDotOrSpace {
			t.Errorf("SegmentProblem(%q) = %v, want TrailingDotOrSpace", name, p)
		}
	}
}

func TestControlRunes(t *testing.T) {
	for _, name := range []string{"plik\x01.txt", "plik\x7f.txt", "nowa\nlinia.txt"} {
		p := SegmentProblem(name)
		if p == nil || p.Kind != ControlRune {
			t.Errorf("SegmentProblem(%q) = %v, want ControlRune", name, p)
		}
	}
}

func TestEmptySegment(t *testing.T) {
	p := SegmentProblem("")
	if p == nil || p.Kind != Empty {
		t.Fatalf("SegmentProblem(\"\") = %v, want Empty", p)
	}
}

// Every Kind must say something specific. A gate that refuses a name while
// explaining nothing is the failure this whole class exists to remove.
func TestEveryKindExplainsItself(t *testing.T) {
	for _, k := range []Kind{ReservedDevice, ReservedRune, ControlRune, Separator, TrailingDotOrSpace, Empty, CaseCollision} {
		if got := (Problem{Kind: k, Detail: "x"}).String(); got == "" || got == "nazwa jest nieprzedstawialna" {
			t.Errorf("Kind %d has no specific explanation: %q", k, got)
		}
	}
}
