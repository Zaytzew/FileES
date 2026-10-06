package explorermenu

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestExplorerClosedFolderGesturesAndRestartDoesNotReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Żółć i zdjęcia")
	for _, verb := range []string{Add, Detach, Download, Release} {
		request, err := Parse([]string{verb, path})
		if err != nil || request.Verb != verb || request.Path != path {
			t.Fatalf("request=%+v err=%v", request, err)
		}
		if got := WithoutRequest([]string{"gui", "--socket", "socket", verb, path}); !reflect.DeepEqual(got, []string{"gui", "--socket", "socket"}) {
			t.Fatalf("replay: %q", got)
		}
	}
	for _, args := range [][]string{nil, {Add}, {Detach, ""}, {Add, "relative"}, {"--delete", path}, {Add, path, "extra"}, {Add, path, Detach, path}, {Add, path + "\n"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
