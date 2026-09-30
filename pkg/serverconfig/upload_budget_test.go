package serverconfig

import (
	"path/filepath"
	"testing"
)

func TestUploadTrashBudgetValidation(t *testing.T) {
	root := t.TempDir()
	u := UploadFile{IntakeRoot: filepath.Join(root, "intake"), TrashRoot: filepath.Join(root, "trash")}
	for _, n := range []int64{0, 1, 10 << 30} {
		u.MaxTrashSize = n
		if err := validateUpload(u, root); err != nil {
			t.Fatal(err)
		}
	}
	u.MaxTrashSize = -1
	if err := validateUpload(u, root); err == nil {
		t.Fatal("negative budget accepted")
	}
	if err := validateUpload(UploadFile{MaxTrashSize: 1}, root); err == nil {
		t.Fatal("budget without intake accepted")
	}
}
