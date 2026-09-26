package contracttests

import (
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/alertchannel"
	"filees/pkg/ipcclient"
	"github.com/google/uuid"
)

func TestServerAlertLocalAckCrossesIPCWithoutProject(t *testing.T) {
	server, _, sock := startEventServer(t)
	realm := uuid.NewString()
	box, err := alertchannel.Open(filepath.Join(t.TempDir(), "alerts.json"), "host", realm)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := (alertchannel.Snapshot{}).Change(realm, "disk.var", "error", "active", "Brak miejsca", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = box.Accept(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	server.SetServerNoticeSource(box)
	client := ipcclient.New(sock, "gui")
	list, err := client.NoticeList(t.Context())
	if err != nil || len(list.Notices) != 1 {
		t.Fatal(list, err)
	}
	n := list.Notices[0]
	if n.Source != "server" || n.ServerID != "host" || n.Status != "active" || n.Severity != "error" || n.Acked || n.Stale || n.ObservedAt == "" || n.RepoID != "" {
		t.Fatal(n)
	}
	if err = client.NoticeAck(t.Context(), n.ID); err != nil {
		t.Fatal(err)
	}
	list, err = client.NoticeList(t.Context())
	if err != nil || !list.Notices[0].Acked {
		t.Fatal(list, err)
	}
	box.Failed()
	list, err = client.NoticeList(t.Context())
	if err != nil || !list.Notices[0].Stale {
		t.Fatal(list, err)
	}
}
