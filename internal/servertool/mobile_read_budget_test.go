//go:build !windows

package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/clientview"
	v1 "filees/pkg/mobile/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestMobileConfiguredReadLimit(t *testing.T) { mobileReadBudgetAcceptance(t, 1) }
func TestMobileConfiguredReadSpace(t *testing.T) { mobileReadBudgetAcceptance(t, 1<<30) }

func mobileReadBudgetAcceptance(t *testing.T, limit int64) {
	t.Helper()
	if isolateSandboxingTest(t, t.Name()) {
		return
	}
	f := newMobileWorkerFixture(t)
	tempRoot, ledger := sandboxTempDir(t), sandboxTempDir(t)
	clientID, repoID := uuid.NewString(), uuid.NewString()
	newMobileSeededRepoAt(t, filepath.Join(f.repositoriesRoot, repoID))
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 9, []clientview.Repository{mobileGrantedRepository(repoID, "r")})
	raw, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{-1, limit} {
		conf["mobile"] = map[string]any{"temp_root": tempRoot, "max_download_size": n}
		raw, _ = json.Marshal(conf)
		if err := os.WriteFile(f.configPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := serverconfig.LoadFor(f.configPath, 0)
		if n < 0 {
			if err == nil || !strings.Contains(err.Error(), "max_download_size") {
				t.Fatalf("negative limit: %v", err)
			}
			continue
		}
		if err != nil || cfg.Mobile.MaxDownloadSize != limit {
			t.Fatalf("config: %+v %v", cfg.Mobile, err)
		}
		if err := os.MkdirAll(filepath.Join(cfg.Repositories.ResultsRoot, "gui-blobs"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: repoID, Path: "photos/a.jpg"})
	header, _ := json.Marshal(req)
	var in, out, stderr bytes.Buffer
	if err := v1.WriteFrame(&in, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}
	if code := runMobileEntry(f.configPath, ledger, []string{"op", clientID}, mobileOperationalGetenv, &in, &out, &stderr, mobileNeverExec(t)); code != ExitOK {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	h, payload, err := v1.ReadFrame(&out, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := v1.ParseResponse(h)
	if err != nil {
		t.Fatal(err)
	}
	if limit == 1 {
		if resp.Error == nil || resp.Error.Code != "download.limit" || len(payload) != 0 {
			t.Fatalf("%+v %q", resp, payload)
		}
	} else if resp.Status != v1.StatusOK || string(payload) != "hello" {
		t.Fatalf("%+v %q %s", resp, payload, &stderr)
	}
	if entries, err := os.ReadDir(tempRoot); err != nil || len(entries) != 0 {
		t.Fatalf("spool leak: %v %v", entries, err)
	}
}
