package historyexport

import (
	"reflect"
	"testing"
	"time"
)

func dir(p string) Node              { return Node{Path: p, Kind: "dir"} }
func file(p string, size int64) Node { return Node{Path: p, Kind: "file", Size: size} }

func TestBuildKeepsTheTreeAndFetchesANameBeforeItsPartSibling(t *testing.T) {
	plan, err := Build([]Node{
		file("top.bin", 10), dir("empty"), file("Docs/a.txt.part", 3), dir("Docs"), file("Docs/a.txt", 5),
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Dirs, []string{"Docs", "empty"}) {
		t.Fatalf("dirs = %v", plan.Dirs)
	}
	want := []File{{"Docs/a.txt", "Docs/a.txt", 5}, {"Docs/a.txt.part", "Docs/a.txt.part", 3}, {"top.bin", "top.bin", 10}}
	if !reflect.DeepEqual(plan.Files, want) || plan.Bytes != 18 {
		t.Fatalf("files = %+v bytes = %d", plan.Files, plan.Bytes)
	}
	if len(plan.Renamed) != 0 || len(plan.Skipped) != 0 || plan.WhaleExcluded {
		t.Fatalf("unexpected changes: %+v", plan)
	}
}

func TestBuildBracketsCaseCollisionsOnlyWhereTheTargetFoldsCase(t *testing.T) {
	nodes := []Node{
		dir("Docs"), file("Docs/x.txt", 1), dir("docs"), file("docs/y.txt", 2),
		file("a.txt", 3), file("A.txt", 4), file("a(A).txt", 5),
	}
	plan, err := Build(nodes, Options{FoldCase: true})
	if err != nil {
		t.Fatal(err)
	}
	wantRenamed := []Rename{{"A.txt", "a(A)_2.txt"}, {"Docs", "docs(Docs)"}}
	if !reflect.DeepEqual(plan.Renamed, wantRenamed) {
		t.Fatalf("renamed = %+v", plan.Renamed)
	}
	locals := map[string]string{}
	for _, f := range plan.Files {
		locals[f.RepoPath] = f.LocalPath
	}
	if locals["a.txt"] != "a.txt" || locals["a(A).txt"] != "a(A).txt" || locals["A.txt"] != "a(A)_2.txt" ||
		locals["Docs/x.txt"] != "docs(Docs)/x.txt" || locals["docs/y.txt"] != "docs/y.txt" {
		t.Fatalf("locals = %v", locals)
	}
	if !reflect.DeepEqual(plan.Dirs, []string{"docs(Docs)", "docs"}) {
		t.Fatalf("dirs = %v", plan.Dirs)
	}

	plain, err := Build(nodes, Options{FoldCase: false})
	if err != nil || len(plain.Renamed) != 0 || len(plain.Files) != 5 {
		t.Fatalf("case-sensitive target renamed something: %+v %v", plain.Renamed, err)
	}
}

func TestBuildSkipsWhatTheTargetCannotHoldAndSaysHowMuch(t *testing.T) {
	plan, err := Build([]Node{
		file("CON.txt", 4),
		dir("a:b"), file("a:b/in.txt", 7), dir("a:b/deep"), file("a:b/deep/z", 2),
		file("trail.", 1),
		dir(".svn"), file(".svn/x", 3),
		dir(".filees-whales"), dir(".filees-whales/g1"), file(".filees-whales/g1/data", 100),
		dir("Docs"), dir("Docs/.filees-whales"), file("Docs/.filees-whales/kept", 6),
		file("ok.txt", 8),
	}, Options{FoldCase: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []Skip{
		{".svn", ReasonWorkingCopyName, 1, 3},
		{"CON.txt", "reserved_device", 1, 4},
		{"a:b", "reserved_rune", 2, 9},
		{"trail.", "trailing_dot_or_space", 1, 1},
	}
	if !reflect.DeepEqual(plan.Skipped, want) {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
	if !plan.WhaleExcluded || plan.WhaleFiles != 1 || plan.WhaleBytes != 100 {
		t.Fatalf("whale = %v %d %d", plan.WhaleExcluded, plan.WhaleFiles, plan.WhaleBytes)
	}
	// Only the root namespace is Whale's; a folder of that name deeper is data.
	wantFiles := []File{{"Docs/.filees-whales/kept", "Docs/.filees-whales/kept", 6}, {"ok.txt", "ok.txt", 8}}
	if !reflect.DeepEqual(plan.Files, wantFiles) || plan.Bytes != 14 {
		t.Fatalf("files = %+v bytes = %d", plan.Files, plan.Bytes)
	}
	if !reflect.DeepEqual(plan.Dirs, []string{"Docs", "Docs/.filees-whales"}) {
		t.Fatalf("dirs = %v", plan.Dirs)
	}
}

func TestBuildLeavesReservedRootNamesToTheExport(t *testing.T) {
	plan, err := Build([]Node{file("FILEES-EKSPORT.txt", 1), dir("Docs"), file("Docs/FILEES-EKSPORT.txt", 2)},
		Options{Reserved: []string{"FILEES-EKSPORT.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Renamed, []Rename{{"FILEES-EKSPORT.txt", "FILEES-EKSPORT_2.txt"}}) {
		t.Fatalf("renamed = %+v", plan.Renamed)
	}
	folded, err := Build([]Node{file("filees-eksport.txt", 1)}, Options{FoldCase: true, Reserved: []string{"FILEES-EKSPORT.txt"}})
	if err != nil || !reflect.DeepEqual(folded.Renamed, []Rename{{"filees-eksport.txt", "filees-eksport_2.txt"}}) {
		t.Fatalf("folded renamed = %+v %v", folded.Renamed, err)
	}
}

func TestBuildRefusesAPlanThatIsNotATree(t *testing.T) {
	for name, nodes := range map[string][]Node{
		"duplicate":           {file("a", 1), file("a", 1)},
		"child without dir":   {file("Docs/a", 1)},
		"child of a file":     {file("Docs", 1), file("Docs/a", 1)},
		"unknown kind":        {{Path: "a", Kind: "link"}},
		"climbing path":       {file("../a", 1)},
		"absolute path":       {file("/a", 1)},
		"empty path":          {file("", 1)},
		"negative size":       {file("a", -1)},
		"empty segment":       {dir("Docs"), file("Docs//a", 1)},
		"dot segment":         {dir("Docs"), file("Docs/./a", 1)},
		"dir child of a file": {file("x", 1), dir("x/y")},
	} {
		if _, err := Build(nodes, Options{}); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestFolderNameIsPortableAndCarriesMomentAndRevision(t *testing.T) {
	moment := time.Date(2026, 9, 12, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	for repo, want := range map[string]string{
		"BFC-TECHNICZNY": "BFC-TECHNICZNY_2026-09-12_10-00-00_r125",
		`a:b/c\d.`:       "a_b_c_d_2026-09-12_10-00-00_r125",
		"CON":            "_CON_2026-09-12_10-00-00_r125",
		"":               "export_2026-09-12_10-00-00_r125",
		"Zażółć gęślą":   "Zażółć gęślą_2026-09-12_10-00-00_r125",
	} {
		if got, err := FolderName(repo, moment, 125); err != nil || got != want {
			t.Errorf("FolderName(%q) = %q %v, want %q", repo, got, err, want)
		}
	}
	if _, err := FolderName("x", time.Time{}, 1); err == nil {
		t.Error("zero moment accepted")
	}
	if _, err := FolderName("x", moment, -1); err == nil {
		t.Error("negative revision accepted")
	}
}
