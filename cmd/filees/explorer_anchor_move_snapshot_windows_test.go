//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/watcher"
)

func TestAnchorTreeMoveSnapshotIsDurableBeforeSendAndRecoveredExactly(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	p := testTreeMoveSnapshot(r, m)
	for i := range 600 {
		p.Entries = append(p.Entries, watcher.PublicationEntry{Path: fmt.Sprintf("%s/%06d.txt", m.Source, i),
			Present: true, Mtime: int64(i), Size: int64(i + 1), Identity: fmt.Sprintf("windows:1:%d:1", i), MD5: "frozen"})
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) <= 16*1024 {
		t.Fatal("not a tree-size fixture", err)
	}
	lost := errors.New("lost remote response")
	svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		got, err := os.ReadFile(filepath.Join(anchorMoveStateDir(r), anchorMoveScannerName))
		if err != nil || !bytes.Equal(raw, got) || !commit.HasPendingRemoteTree(r.LocalPath) {
			t.Fatal("observations were not durable before remote effect", err)
		}
		return nil, lost
	}, inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		return &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}, nil
	}}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, p, svn); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	// A fresh caller must recover the old observations, not its changed inputs.
	p.Entries[1].Size++
	p.Session = "a later scanner"
	for range 2 {
		got, err := inspectAnchorTreeMoveState(t.Context(), r, svn)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := json.Marshal(got.Snapshot)
		if err != nil || !bytes.Equal(raw, decoded) || got.Receipt.Revision != 20 {
			t.Fatal("recovery replaced frozen observations", err)
		}
	}
	if svn.sends != 1 || svn.inspections != 2 || !commit.HasPendingRemoteTree(r.LocalPath) {
		t.Fatal("inspection replayed or retired the move")
	}
}

func TestAnchorTreeMoveSnapshotDecoderBoundsEntriesWhileReading(t *testing.T) {
	tooMany := `{"Entries":[` + strings.Repeat("{},", 100000) + `{}]}`
	d := json.NewDecoder(strings.NewReader(tooMany))
	d.DisallowUnknownFields()
	if _, err := decodeAnchorMoveSnapshot(d); err == nil || !strings.Contains(err.Error(), "too many baseline entries") {
		t.Fatal("entry allocation was not bounded during parse", err)
	}
	for _, raw := range []string{`{"Entries":null}`, `{"Sequence":1,"Sequence":2}`, `{"schema":"future"}`, `[]`,
		`{"Entries":[{"unknown":true}]}`, `{"Entries":[]`, `{"Sequence":"not a generation"}`} {
		d := json.NewDecoder(strings.NewReader(raw))
		d.DisallowUnknownFields()
		if _, err := decodeAnchorMoveSnapshot(d); err == nil {
			t.Fatal("invalid snapshot representation accepted", raw)
		}
	}
}

func TestAnchorTreeMoveSnapshotDamageNeverReachesSVN(t *testing.T) {
	for _, mode := range []string{"missing", "changed", "unknown-field", "trailing", "whitespace", "oversized", "legacy", "invalid-digest"} {
		t.Run(mode, func(t *testing.T) {
			r, m := testTreeMoveIntent(t)
			svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, errors.New("uncertain") },
				inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
					t.Fatal("damaged observations reached SVN")
					return nil, nil
				}}
			_, _ = sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn)
			file := filepath.Join(anchorMoveStateDir(r), anchorMoveScannerName)
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				err = os.Remove(file)
			case "changed":
				err = os.WriteFile(file, bytes.Replace(raw, []byte(`"Sequence":1`), []byte(`"Sequence":2`), 1), 0600)
			case "unknown-field":
				err = os.WriteFile(file, append(raw[:len(raw)-1], []byte(`,"untrusted":true}`)...), 0600)
			case "trailing":
				err = os.WriteFile(file, append(raw, []byte(" {}")...), 0600)
			case "whitespace":
				err = os.WriteFile(file, append(raw, ' '), 0600) // EVERY byte is bound.
			case "oversized":
				err = os.Truncate(file, anchorMoveScannerLimit+1)
			case "legacy", "invalid-digest":
				path := filepath.Join(anchorMoveStateDir(r), commit.RemoteTreePendingName)
				var in anchorMoveIntent
				if err := readAnchorMoveJSON(t.Context(), path, &in); err != nil {
					t.Fatal(err)
				}
				if mode == "legacy" {
					in.Schema = "filees.anchor-move-intent/v1"
				} else {
					in.ScannerSHA256 = "unbound"
				}
				changed, encodeErr := json.Marshal(in)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				err = os.WriteFile(path, changed, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspectAnchorTreeMoveState(t.Context(), r, svn); err == nil || svn.inspections != 0 || svn.sends != 1 || !commit.HasPendingRemoteTree(r.LocalPath) {
				t.Fatal("damaged snapshot was accepted, resent or unfenced", err)
			}
		})
	}
}

func TestAnchorTreeMoveSnapshotBindingAndPreparedCrash(t *testing.T) {
	for _, mode := range []string{"nil", "wc", "source", "destination", "entries", "identity", "snapshot-obstruction"} {
		t.Run(mode, func(t *testing.T) {
			r, m := testTreeMoveIntent(t)
			p := testTreeMoveSnapshot(r, m)
			switch mode {
			case "nil":
				p = nil
			case "wc":
				p.WC = filepath.Join(r.LocalPath, "foreign")
			case "source":
				p.Source = "foreign"
			case "destination":
				p.Destination = "foreign"
			case "entries":
				p.Entries = nil
			case "identity":
				p.Entries[0].Identity = string(bytes.Repeat([]byte{'x'}, 1025))
			case "snapshot-obstruction":
				if err := os.Mkdir(filepath.Join(anchorMoveStateDir(r), anchorMoveScannerName), 0700); err != nil {
					t.Fatal(err)
				}
			}
			svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
				t.Fatal("unbound snapshot sent")
				return nil, nil
			}}
			if _, err := sendAnchorTreeMove(t.Context(), r, m, p, svn); err == nil || svn.sends != 0 {
				t.Fatal(err)
			}
		})
	}
	// Persisted intent, then interrupted before snapshot/request. Preserve the
	// fence; neither a fresh scan nor an old schema authorizes an automatic send.
	r, m := testTreeMoveIntent(t)
	guards, root, err := guardAnchorMoveState(r)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := anchorMoveSnapshotDigest(testTreeMoveSnapshot(r, m), r, m)
	if err != nil {
		t.Fatal(err)
	}
	in := anchorMoveIntent{Schema: "filees.anchor-move-intent/v2", ServerID: r.ServerID, RepoID: r.RepoID,
		AttachmentID: r.OperationID, WC: r.LocalPath, Root: root, Move: m, ScannerSHA256: digest}
	err = writeAnchorReleaseJSONFile(anchorMoveStateDir(r), commit.RemoteTreePendingName, in)
	closeAnchorReleaseGuards(guards)
	if err != nil {
		t.Fatal(err)
	}
	svn := &testTreeMover{}
	if _, err := inspectAnchorTreeMoveState(t.Context(), r, svn); err == nil || svn.inspections != 0 || !commit.HasPendingRemoteTree(r.LocalPath) {
		t.Fatal("prepared crash was unfenced or reached SVN", err)
	}
}

func TestAnchorTreeMovePersistedSnapshotFeedsRealScannerRecovery(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	old := filepath.Join(r.LocalPath, m.Source)
	newPath := filepath.Join(r.LocalPath, filepath.FromSlash(m.Destination))
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "work.txt"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(anchorMoveStateDir(r), "manifest.json")
	scanner, err := watcher.NewScanner(watcher.Options{WC: r.LocalPath, StatePath: manifest, UseMD5: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	events := scanner.Start(ctx)
	// Capture waits on the real scan guard; initial scan publishes its baseline.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := scanner.CapturePublication([]string{m.Source})
		if err != nil {
			t.Fatal(err)
		}
		if p.Sequence > 0 {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("scanner did not complete initial scan")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	for range events {
	} // Drain final scan; fixture scanner has no live worker.
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("remote reply lost")
	svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, lost },
		inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
			return &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}, nil
		}}
	service := &commit.Service{Cli: client.New(client.Options{})}
	service.BindWorkingCopy(r.RepoID, r.LocalPath)
	service.FenceTreeMove = scanner.WithTreeMove
	if err := service.WithRemoteTreeMove(t.Context(), r.LocalPath, m.Source, m.Destination, nil, func(p *watcher.TreeMoveSnapshot) error {
		_, err := sendAnchorTreeMove(t.Context(), r, m, p, svn)
		return err
	}); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	// Plain-filesystem effect fixture only: not native SVN/CFAPI reconciliation.
	if err := os.Rename(old, newPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newPath, "work.txt"), []byte("later unpublished edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(manifest); err != nil || !bytes.Equal(got, before) {
		t.Fatal("uncertain move replaced the baseline", err)
	}
	state, err := inspectAnchorTreeMoveState(t.Context(), r, svn)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := watcher.NewScanner(watcher.Options{WC: r.LocalPath, StatePath: manifest})
	if err != nil {
		t.Fatal(err)
	}
	service.RecoverTreeMove = fresh.WithTreeMoveRecovery
	if err := service.WithRemoteTreeMove(t.Context(), r.LocalPath, m.Source, m.Destination, state.Snapshot, func(p *watcher.TreeMoveSnapshot) error {
		if !reflect.DeepEqual(p, state.Snapshot) {
			t.Fatal("recovery discarded persisted observations")
		}
		return nil // Exact filesystem effect fixture, not production effect proof.
	}); err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Path string
		Size int64
	}
	raw, err := os.ReadFile(manifest)
	if err != nil || json.Unmarshal(raw, &entries) != nil {
		t.Fatal("bad ACK baseline", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Path == m.Destination+"/work.txt" {
			found = true
			if entry.Size != int64(len("published")) {
				t.Fatal("later edit falsely acknowledged")
			}
		}
	}
	if !found || !commit.HasPendingRemoteTree(r.LocalPath) || svn.sends != 1 {
		t.Fatal("baseline not relocated, fence retired or remote replayed")
	}
}
