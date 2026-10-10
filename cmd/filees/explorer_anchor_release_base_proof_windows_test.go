//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAnchorReleaseBaseProofBindsEveryRetainedIdentity(t *testing.T) {
	for _, name := range []string{"valid", "missing", "version", "phase", "id", "repo", "url", "revision", "path", "wc", "root", "source", "digest", "size", "copy", "replacement", "malformed", "unknown", "trailing", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			j, body := newTestAnchorReleaseJournal(t)
			requestTestReleaseExclusion(t, j)
			r, err := j.inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(j.store, j.id, "data.bin")
			f, info, err := openAnchorReleaseFile(path, false)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			copy := anchorReleaseID(info)
			proof := anchorReleaseRetirement{Version: 2, Record: r, Copy: copy}
			switch name {
			case "version":
				proof.Version = 1
			case "phase":
				proof.Record.Phase = "prepare_requested"
			case "id":
				proof.Record.ID += "-other"
			case "repo":
				proof.Record.Identity.RepoUUID += "-other"
			case "url":
				proof.Record.Identity.RepoURL += "-other"
			case "revision":
				proof.Record.Identity.Revision++
			case "path":
				proof.Record.Identity.Rel += "-other"
			case "wc":
				proof.Record.Identity.WC += "-other"
			case "root":
				proof.Record.Root.Index++
			case "source":
				proof.Record.Source.Index++
			case "digest":
				proof.Record.SHA256 = "different"
			case "size":
				proof.Record.Size++
			case "copy":
				proof.Copy.Index++
			}
			proofPath := filepath.Join(j.store, j.id, anchorReleaseBaseProofName)
			if name != "missing" {
				if err := writeAnchorReleaseJSONFile(filepath.Dir(proofPath), anchorReleaseBaseProofName, proof); err != nil {
					t.Fatal(err)
				}
			}
			if name == "malformed" || name == "unknown" || name == "trailing" {
				raw, _ := json.Marshal(proof)
				switch name {
				case "malformed":
					raw = []byte("{")
				case "unknown":
					raw = append([]byte(`{"Extra":true,`), raw[1:]...)
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				}
				if err := os.WriteFile(proofPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "replacement" {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				f, info, err = openAnchorReleaseFile(path, false)
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
				copy = anchorReleaseID(info)
			}
			if err := j.request(t.Context(), "restore_requested"); err != nil {
				t.Fatal(err)
			}
			r, err = j.inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			verified, err := j.verifiedBase(ctx, r, copy)
			if name == "valid" {
				if err != nil || !verified {
					t.Fatal("valid proof refused", err)
				}
			} else if name == "missing" {
				if err != nil || verified {
					t.Fatal("missing proof inferred", err)
				}
			} else if err == nil || verified {
				t.Fatal("different proof accepted", name, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
				t.Fatal("inspection changed retained bytes", err)
			}
		})
	}
}

func TestAnchorReleaseBaseProofWriteIsImmutableAndPhaseBound(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	r, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	copy := anchorReleaseFileID{Volume: 1, Index: 2}
	if err := j.recordBaseProof(t.Context(), r, copy); err == nil {
		t.Fatal("request before exclusion accepted")
	}
	requestTestReleaseExclusion(t, j)
	r, err = j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.recordBaseProof(t.Context(), r, copy); err != nil {
		t.Fatal(err)
	}
	if err := j.recordBaseProof(t.Context(), r, copy); err == nil {
		t.Fatal("receipt was replaced")
	}
	if err := j.request(t.Context(), "restore_requested"); err != nil {
		t.Fatal(err)
	}
	r, err = j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := j.verifiedBase(t.Context(), r, copy); err != nil || !verified {
		t.Fatal("receipt lost", err)
	}
}
