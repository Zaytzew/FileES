package commit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type datedLogClient struct {
	client.Client
	calls int
	date  string
}

func (c *datedLogClient) LogMessages(_ context.Context, _ string, from, to int64) ([]client.LogMessage, error) {
	c.calls++
	return []client.LogMessage{{Revision: to, Date: c.date}}, nil
}
func TestLastCommitCacheUsesSVNDateAndRevision(t *testing.T) {
	wc := t.TempDir()
	cli := &datedLogClient{date: "2026-07-01T10:00:00.123456Z"}
	s := &Service{Cli: cli, wc: wc}
	s.recordLastCommit(t.Context(), wc, 7)
	path := filepath.Join(wc, ".filees", "state", "last_commit.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Revision int64
		Date     string
	}
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 7 || got.Date != cli.date {
		t.Fatalf("metadata=%+v", got)
	}
	s.recordLastCommit(t.Context(), wc, 7)
	if cli.calls != 1 {
		t.Fatal("refetched unchanged revision")
	}
	cli.date = "not a date"
	s.recordLastCommit(t.Context(), wc, 8)
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(raw) {
		t.Fatal("invalid date replaced evidence")
	}
	cli.date = "2026-08-01T11:00:00Z"
	s.recordLastCommit(t.Context(), wc, 8)
	newer, _ := os.ReadFile(path)
	if string(newer) == string(raw) {
		t.Fatal("new revision not recorded")
	}
}

type datedTransactionClient struct{ *transactionFake }

func (c *datedTransactionClient) LogMessages(_ context.Context, _ string, _, to int64) ([]client.LogMessage, error) {
	return []client.LogMessage{{Revision: to, Date: "2026-07-01T10:00:00Z"}}, nil
}
func TestLastCommitDurableReceiptAlsoRecordsDate(t *testing.T) {
	s, cli, _, wc := transactionFixture(t)
	s.Cli = &datedTransactionClient{cli}
	if _, err := s.RequestPublish(t.Context(), wc, "dated"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wc, ".filees", "state", "last_commit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Revision int64
		Date     string
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Revision != 5 || got.Date != "2026-07-01T10:00:00Z" {
		t.Fatalf("receipt date: %s %v", raw, err)
	}
}
