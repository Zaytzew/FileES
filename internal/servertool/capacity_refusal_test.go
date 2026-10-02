package servertool

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestCapacityRefusalIsTypedAndNeverBindsOperation(t *testing.T) {
	c := serverconfig.Config{}
	c.Activation.ServiceWorkingCopy = t.TempDir()
	c.Repositories.ResultsRoot = filepath.Join(t.TempDir(), "not-created")
	client := uuid.NewString()
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Synthetic server", ClientID: client, RealmID: uuid.NewString(), ClientRole: "normal", Generation: 1, GeneratedAt: time.Now()}
	if _, err := clientview.StoreIfNewer(filepath.Join(c.Activation.ServiceWorkingCopy, "clients", client, "view.json"), v); err != nil {
		t.Fatal(err)
	}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreateRepository, client, control.CreateRepositoryPayload{Name: "example"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ticket)
	var out bytes.Buffer
	for i := 0; i < 2; i++ {
		out.Reset()
		if code := serveCapacityRefusal(c, client, bytes.NewReader(raw), &out, io.Discard); code != ExitOK {
			t.Fatal(code)
		}
		var result control.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		if result.OperationID != ticket.OperationID || result.Error.Code != "STORAGE_INSUFFICIENT" {
			t.Fatal(result)
		}
	}
	if _, err := os.Stat(c.Repositories.ResultsRoot); !os.IsNotExist(err) {
		t.Fatal("created operation state", err)
	}
	if code := serveCapacityRefusal(c, uuid.NewString(), bytes.NewReader(raw), io.Discard, io.Discard); code == ExitOK {
		t.Fatal("unbound actor accepted")
	}
}
