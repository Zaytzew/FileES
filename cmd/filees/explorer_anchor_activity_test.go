//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/activity"
	"filees/pkg/localrepo"
)

func TestAnchorActivityOnlyAfterSuccessfulHydrationAndAdoption(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "failed", false: "received"}[fail], func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "downloaded")
			if err := os.WriteFile(file, []byte("bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			journal, err := activity.Open(filepath.Join(root, "journal.json"), 20)
			if err != nil {
				t.Fatal(err)
			}
			notified := make(chan string, 1)
			m := &anchorManager{activity: journal, activityChanged: func(repoID string) { notified <- repoID }}
			adopted := make(chan struct{}, 1)
			bridge := &anchorBridge{
				root: root, retainLocal: true,
				materialize: func(context.Context, string) (string, int64, error) { return file, 23, nil },
				adopt: func(context.Context, string, int64) error {
					adopted <- struct{}{}
					if fail {
						return errors.New("failed")
					}
					return nil
				},
				received: func(rel string, rev int64, size *int64) {
					m.recordHydration(localrepo.Record{RepoID: "repo"}, rel, rev, size)
				},
			}
			helper, _ := runAnchorBridge(t, bridge)
			helper.fetch(t, "1", "folder/file.dwg")
			if answer := helper.answer(t); answer[0] != "ok" {
				t.Fatal(answer)
			}
			if len(journal.List()) != 0 {
				t.Fatal("receipt before hydration")
			}
			io.WriteString(helper.toDaemon, "hydrated\t1\tfolder/file.dwg\n")
			<-adopted
			if fail {
				select {
				case <-notified:
					t.Fatal("failed adoption notified")
				case <-time.After(50 * time.Millisecond):
				}
				if len(journal.List()) != 0 {
					t.Fatal("failed adoption journalled")
				}
				return
			}
			select {
			case repo := <-notified:
				if repo != "repo" {
					t.Fatal(repo)
				}
			case <-time.After(time.Second):
				t.Fatal("no change event")
			}
			rows := journal.List()
			if len(rows) != 1 || rows[0].Stage != activity.Received || rows[0].Revision != 23 || rows[0].Size == nil || *rows[0].Size != 5 {
				t.Fatal(rows)
			}
			m.recordHydration(localrepo.Record{RepoID: "repo"}, "folder/file.dwg", 23, nil)
			select {
			case <-notified:
				t.Fatal("duplicate event")
			default:
			}
		})
	}
}
