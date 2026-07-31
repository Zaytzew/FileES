package slug

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAcceptsRealisticSlugs(t *testing.T) {
	// Taken from the live PERMALINKS registry, which is the shape these are
	// actually used in.
	for _, in := range []string{
		"petofiego",
		"kochanowskiego8",
		"pfu-szegedynska",
		"wom-grafika",
		"sosnowa-plonsk",
		"bonifacio-archiwalna",
		"sggw-upload",
	} {
		got, err := Normalize(in)
		if err != nil {
			t.Fatalf("Normalize(%q) = %v, want accepted", in, err)
		}
		if got != in {
			t.Fatalf("Normalize(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestNormalizeLowercasesAndTrims(t *testing.T) {
	got, err := Normalize("  Wydanie-Pierwsze  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wydanie-pierwsze" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string]struct {
		in   string
		want error
	}{
		"empty":         {"", ErrEmpty},
		"blank":         {"   ", ErrEmpty},
		"too short":     {"ab", ErrLength},
		"too long":      {strings.Repeat("a", MaxLen+1), ErrLength},
		"leading dash":  {"-wydanie", ErrEdge},
		"trailing dash": {"wydanie-", ErrEdge},
		"double dash":   {"wydanie--pierwsze", ErrDoubleDash},
		"underscore":    {"wydanie_pierwsze", ErrCharset},
		"dot":           {"wydanie.pierwsze", ErrCharset},
		"slash":         {"wydanie/pierwsze", ErrCharset},
		"space inside":  {"wydanie pierwsze", ErrCharset},
		"reserved":      {"file", ErrReserved},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("Normalize(%q) = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

// A slug that survived normalization must never be able to escape its own path
// segment, because it is concatenated into a URL path.
func TestNormalizedSlugIsASinglePathSegment(t *testing.T) {
	for _, in := range []string{
		"../etc",
		"a/../b",
		"a%2fb",
		"a\\b",
		"a\x00b",
	} {
		if _, err := Normalize(in); err == nil {
			t.Fatalf("Normalize(%q) was accepted", in)
		}
	}
}

func TestPath(t *testing.T) {
	got, err := Path("atmprojekt", "petofiego")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/atmprojekt/petofiego" {
		t.Fatalf("got %q", got)
	}
}

func TestPathRejectsAliasWithSeparator(t *testing.T) {
	for _, alias := range []string{"", "  ", "atm/projekt", "atm\\projekt"} {
		if _, err := Path(alias, "petofiego"); err == nil {
			t.Fatalf("Path(%q, ...) was accepted", alias)
		}
	}
}

func TestPathRejectsBadSlug(t *testing.T) {
	if _, err := Path("atmprojekt", "file"); !errors.Is(err, ErrReserved) {
		t.Fatalf("got %v, want ErrReserved", err)
	}
}
