package client

import "testing"

func TestPegSafe(t *testing.T) {
	cases := map[string]string{
		"@Unieważnione":     "@Unieważnione@",
		"@Cancelled":        "@Cancelled@",
		"a@b":               "a@b@",
		"foo@":              "foo@@",
		"sub/@dir/file.pdf": "sub/@dir/file.pdf@",
		"plain.pdf":         "plain.pdf",
		"":                  "",
		".":                 ".",
	}
	for in, want := range cases {
		if got := pegSafe(in); got != want {
			t.Fatalf("pegSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
