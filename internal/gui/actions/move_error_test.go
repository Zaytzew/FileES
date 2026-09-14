package actions

import "testing"

func TestMoveBlockedByOpenHandleRecognizesWindowsRenameErrors(t *testing.T) {
	for _, message := range []string{
		`rename F:\old F:\new: Access is denied.`,
		"The process cannot access the file because it is being used by another process.",
		"sharing violation",
	} {
		if !moveBlockedByOpenHandle(message) {
			t.Errorf("did not recognize %q", message)
		}
	}
	if moveBlockedByOpenHandle("destination folder must not exist") {
		t.Fatal("unrelated relocation error recognized as an open handle")
	}
}
