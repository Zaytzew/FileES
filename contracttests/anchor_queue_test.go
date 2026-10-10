package contracttests

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filees/pkg/anchorqueue"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"github.com/google/uuid"
)

type contractQueueProbe struct {
	contractBatchProbe
	store  *anchorqueue.Store
	root   string
	queues int
}

func (q *contractQueueProbe) Queue(_ context.Context, p contract.RepoAnchorQueuePayload) (contract.RepoAnchorQueueResult, error) {
	q.queues++
	x, err := q.store.Enqueue(p, "operation", "file:///repo", q.root)
	return x.Result, err
}
func (q *contractQueueProbe) QueuedReleases() []contract.RepoAnchorQueueResult {
	var out []contract.RepoAnchorQueueResult
	for _, x := range q.store.List() {
		out = append(out, x.Result)
	}
	return out
}

func TestAnchorReleaseQueueContractIsDurableScopedAndDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "queue.json")
	store, err := anchorqueue.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &contractQueueProbe{store: store, root: root}
	socket := testSocketPath(t)
	s := ipcserver.New(socket)
	s.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	s.RegisterRepoAccess("repo", "file:///repo", root, "server", "r")
	s.SetAnchorBatch(q)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(socket, "queue-test")
	hello, err := c.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoAnchorReleaseQueue) {
		t.Fatal(hello, err)
	}
	p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: 3, RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32), RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: "release"}}}
	for range 2 {
		result, err := c.AnchorReleaseQueue(ctx, p)
		if err != nil || result.State != "queued" || result.IntentID != p.IntentID {
			t.Fatal(result, err)
		}
	}
	q.store, err = anchorqueue.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	status := contract.RepoAnchorQueueStatusPayload{ServerID: "server", RepoID: "repo", IntentID: p.IntentID}
	result, err := c.AnchorReleaseQueueStatus(ctx, status)
	if err != nil || result.Total != 3 || result.State != "queued" || q.writes != 0 || len(q.store.List()) != 1 {
		t.Fatal(result, err, q.writes)
	}
	for _, mutate := range []func(*contract.RepoAnchorQueuePayload){func(p *contract.RepoAnchorQueuePayload) { p.Action = "download" }, func(p *contract.RepoAnchorQueuePayload) { p.Fingerprint = "" }, func(p *contract.RepoAnchorQueuePayload) { p.Path = "../a" }, func(p *contract.RepoAnchorQueuePayload) { p.ServerID = "other" }, func(p *contract.RepoAnchorQueuePayload) { p.Files = 0 }} {
		bad := p
		mutate(&bad)
		if _, err := c.AnchorReleaseQueue(ctx, bad); err == nil {
			t.Fatal("invalid intention admitted", bad)
		}
	}
	status.ServerID = "other"
	if _, err := c.AnchorReleaseQueueStatus(ctx, status); err == nil {
		t.Fatal("wrong activation exposed result")
	}
	if q.queues != 2 {
		t.Fatal("invalid payload reached queue", q.queues)
	}
	system, err := c.SystemStatus(ctx)
	if err != nil || len(system.AnchorReleaseQueue) != 1 {
		t.Fatal(system, err)
	}
	s.SetAnchorBatch(&q.contractBatchProbe)
	hello, err = c.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorReleaseQueue) {
		t.Fatal("legacy service advertised durable queue", hello, err)
	}
}
