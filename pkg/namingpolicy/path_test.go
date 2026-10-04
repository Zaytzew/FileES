package namingpolicy

import "testing"

func TestBrowserRelativePath(t *testing.T) {
	for source, want := range map[string]string{
		"Oferta/Opinia Łódź.pdf":            "Oferta/Opinia-Lodz.pdf",
		"Oferta 1/Rysunki końcowe/plan.pdf": "Oferta-1/Rysunki-koncowe/plan.pdf",
		"plan.pdf":                          "plan.pdf",
	} {
		name := source
		for i := len(source) - 1; i >= 0; i-- {
			if source[i] == '/' {
				name = source[i+1:]
				break
			}
		}
		got, err := TargetPath(name, source)
		if err != nil || got != want {
			t.Fatalf("%q: %q, %v", source, got, err)
		}
	}
	for _, path := range []string{"/plan.pdf", "a//plan.pdf", "../plan.pdf", "a/../plan.pdf", "a\\plan.pdf", "C:/plan.pdf", ".svn/plan.pdf", ".filees/plan.pdf", "x\u202e/plan.pdf", "a/other.pdf"} {
		if got, err := TargetPath("plan.pdf", path); err == nil {
			t.Errorf("accepted %q -> %q", path, got)
		}
	}
}
