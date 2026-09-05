package reservationclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	reservationv1 "filees/pkg/reservation/v1"
	"testing"
	"time"
)

func TestFetchStateUsesExistingPinnedSSHAndStrictV2(t *testing.T) {
	host, _ := generateKey(t)
	signer, private := generateKey(t)
	now := time.Now().UTC()
	address := startReservationSSH(t, host, signer.PublicKey(), func(in *bufio.Reader, out *bytes.Buffer) {
		line, _ := in.ReadBytes('\n')
		req, err := reservationv1.ParseRequest(bytes.TrimSpace(line))
		if err != nil || req.Schema != reservationv1.StateSchema {
			return
		}
		r := reservationv1.Result{Schema: req.Schema, RepoID: req.RepoID, RepositoryState: "deleted", ViewGeneration: 8, ViewGeneratedAt: &now}
		raw, _ := json.Marshal(r)
		out.Write(append(raw, '\n'))
	})
	c := configuredClient(t, address, host.PublicKey(), private)
	r, err := c.FetchState(context.Background(), testRepoID)
	if err != nil || r.RepositoryState != "deleted" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}
