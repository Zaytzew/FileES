package anchorrepair

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeSystem struct {
	provider   string
	tree       map[string][]Entry // directory relative to root -> entries
	unreadable map[string]bool    // relative path
	detachErr  error
	attachErr  error
	removeErr  map[string]error
	calls      []string
	root       string
}

func (f *fakeSystem) rel(path string) string {
	rel, _ := filepath.Rel(f.root, path)
	if rel == "." {
		return ""
	}
	return rel
}
func (f *fakeSystem) SyncRootProvider(string) (string, error) { return f.provider, nil }
func (f *fakeSystem) List(directory string) ([]Entry, error)  { return f.tree[f.rel(directory)], nil }
func (f *fakeSystem) Unreadable(path string) (bool, error)    { return f.unreadable[f.rel(path)], nil }
func (f *fakeSystem) Detach(volume string) error {
	f.calls = append(f.calls, "detach "+volume)
	return f.detachErr
}
func (f *fakeSystem) Attach(volume string) error {
	f.calls = append(f.calls, "attach "+volume)
	return f.attachErr
}
func (f *fakeSystem) RemoveCloudEntry(_, path string, directory bool) error {
	kind := "file"
	if directory {
		kind = "dir"
	}
	f.calls = append(f.calls, "remove "+kind+" "+f.rel(path))
	return f.removeErr[f.rel(path)]
}

const testRoot = `F:\work\anchor`

func damagedAnchor() *fakeSystem {
	cloudFile := func(name string) Entry { return Entry{Name: name, Reparse: true, Cloud: true} }
	cloudDir := func(name string) Entry { return Entry{Name: name, Directory: true, Reparse: true, Cloud: true} }
	return &fakeSystem{
		provider: Provider, root: testRoot,
		tree: map[string][]Entry{
			"":          {{Name: ".svn", Directory: true}, {Name: ".filees", Directory: true}, cloudDir("02"), cloudFile("ok.pdf"), {Name: "mine.txt"}, {Name: "link", Directory: true, Reparse: true}},
			"02":        {cloudFile("good.pdf"), cloudFile("bad.pdf"), cloudDir("Zegrze"), cloudDir("fine")},
			`02\fine`:   {cloudFile("deep-bad.pdf")},
			"link":      {cloudFile("outside.pdf")},
			".svn":      {cloudFile("never.pdf")},
			`02\Zegrze`: {cloudFile("unreachable.pdf")},
		},
		unreadable: map[string]bool{`02\bad.pdf`: true, `02\Zegrze`: true, `02\fine\deep-bad.pdf`: true, `link\outside.pdf`: true, `.svn\never.pdf`: true},
	}
}

func TestRepairRemovesOnlyWhatItFoundUnreadableAndAlwaysAttachesAgain(t *testing.T) {
	system := damagedAnchor()
	result := Repair(system, testRoot, "run-1")
	if !result.Complete() || result.Found != 3 || !result.Detached || !result.Reattached {
		t.Fatalf("result: %+v", result)
	}
	if want := []string{`02\Zegrze`, `02\bad.pdf`, `02\fine\deep-bad.pdf`}; !reflect.DeepEqual(result.Removed, want) {
		t.Fatalf("removed %q, want %q", result.Removed, want)
	}
	want := []string{"detach F:", `remove file 02\fine\deep-bad.pdf`, `remove file 02\bad.pdf`, `remove dir 02\Zegrze`, "attach F:"}
	if !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("calls %q, want %q", system.calls, want)
	}
}

func TestRepairLeavesTheFilterAloneWhenThereIsNothingToDo(t *testing.T) {
	system := damagedAnchor()
	system.unreadable = nil
	if result := Repair(system, testRoot, ""); !result.Complete() || result.Detached || len(system.calls) != 0 {
		t.Fatalf("result %+v, calls %q", result, system.calls)
	}
}

func TestRepairRefusesWhatIsNotAFileESFolder(t *testing.T) {
	for name, change := range map[string]func(*fakeSystem) string{
		"another provider": func(f *fakeSystem) string { f.provider = "OneDrive"; return testRoot },
		"volume root":      func(*fakeSystem) string { return `F:\` },
		"relative":         func(*fakeSystem) string { return `work\anchor` },
		"network share":    func(*fakeSystem) string { return `\server\share\anchor` },
		"unclean":          func(*fakeSystem) string { return `F:\work\..\work\anchor` },
	} {
		t.Run(name, func(t *testing.T) {
			system := damagedAnchor()
			result := Repair(system, change(system), "")
			if result.Error == "" || result.Detached || len(system.calls) != 0 {
				t.Fatalf("result %+v, calls %q", result, system.calls)
			}
		})
	}
}

func TestRepairWithoutAdministratorRightsChangesNothing(t *testing.T) {
	system := damagedAnchor()
	system.detachErr = ErrElevationNeeded
	result := Repair(system, testRoot, "")
	if result.Error != "elevation_required" || result.Detached || len(result.Removed) != 0 || !reflect.DeepEqual(system.calls, []string{"detach F:"}) {
		t.Fatalf("result %+v, calls %q", result, system.calls)
	}
}

func TestRepairReportsWhatStayedAndAFilterThatDidNotComeBack(t *testing.T) {
	system := damagedAnchor()
	system.removeErr = map[string]error{`02\Zegrze`: errors.New("directory is not empty")}
	result := Repair(system, testRoot, "")
	if result.Complete() || result.Error != "remove" || !result.Reattached || !reflect.DeepEqual(result.Remaining, []string{`02\Zegrze`}) || len(result.Removed) != 2 {
		t.Fatalf("result: %+v", result)
	}
	if system.calls[len(system.calls)-1] != "attach F:" {
		t.Fatalf("filter was not attached again: %q", system.calls)
	}

	system = damagedAnchor()
	system.attachErr = errors.New("0x801f0013")
	result = Repair(system, testRoot, "")
	if result.Complete() || result.Error != "attach" || !result.Detached || result.Reattached || len(result.Removed) != 3 {
		t.Fatalf("result: %+v", result)
	}
	if again := Reattach(damagedAnchor(), testRoot, ""); !again.Reattached || again.Error != "" {
		t.Fatalf("reattach: %+v", again)
	}
}
