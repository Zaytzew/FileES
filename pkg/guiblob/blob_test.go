package guiblob

import (
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBlobBoundsAndIdentity(t *testing.T) {
	for _, write := range []Write{{Data: strings.Repeat("x", MaxBytes+1)}, {Data: strings.Repeat("\x00", MaxBytes)}, {Expected: "../realm"}, {Data: string([]byte{255})}} {
		if write.Validate() == nil {
			t.Fatal("accepted invalid blob")
		}
	}
	if err := (Write{Data: strings.Repeat("x", MaxBytes)}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (State{RealmID: uuid.NewString(), Data: "unversioned"}).Validate(); err == nil {
		t.Fatal("unversioned content accepted")
	}
}
