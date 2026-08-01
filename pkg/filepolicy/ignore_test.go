package filepolicy

import "testing"

func TestBuiltinIgnorePolicy(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"~$report.docx", true},
		{"sub/.~lock.report.doc#", true},
		{"archive/old.bak", true},
		{"src/node_modules", true},
		{"src/node_modules/package.json", false}, // callers skip an ignored directory
		{".git", true},
		{"report.docx", false},
	}
	for _, tc := range cases {
		if got := IsBuiltinIgnored(tc.path); got != tc.want {
			t.Errorf("IsBuiltinIgnored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
