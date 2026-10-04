package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/uploadworker"
	"filees/pkg/avscan"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/realmbranding"
	"filees/pkg/repoworker"
	"filees/public-shares/authority"
	"filees/public-shares/backchannel"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
	"filees/public-shares/manifest"
	"filees/public-shares/web"
	"github.com/google/uuid"
)

func freshShelfRepoID() string { return uuid.NewString() }

type shelfHTTPAuthority struct{ owner, parent string }

func (a shelfHTTPAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.parent {
		return fmt.Errorf("not owner")
	}
	return nil
}
func (a shelfHTTPAuthority) ActiveRealmAlias(owner string) (string, error) {
	if owner != a.owner {
		return "", fmt.Errorf("not owner")
	}
	return "shelf-test", nil
}
func (a shelfHTTPAuthority) ActiveRealmBranding(owner string) (realmbranding.Branding, error) {
	if owner != a.owner {
		return realmbranding.Branding{}, fmt.Errorf("not owner")
	}
	return realmbranding.Default(), nil
}

type shelfHTTPConsumerAuthority struct {
	shelfDesktopAuthority
	channels         *channel.Store
	owner, channelID string
}

func (a shelfHTTPConsumerAuthority) ListShelf(ctx context.Context, _, _ string) (contract.ShelfListResult, error) {
	result, err := (repoworker.ChannelUploadService{Channels: a.channels}).ListShelf(ctx, a.owner, a.channelID)
	if err != nil {
		return contract.ShelfListResult{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return contract.ShelfListResult{}, err
	}
	var out contract.ShelfListResult
	err = json.Unmarshal(data, &out)
	out.ChannelID = "channel"
	return out, err
}

// HTTP -> protected backchannel -> intake -> real clamscan -> SVN -> durable
// accepted index -> owner's ListShelf. The caller continues via actual IPC,
// native sparse checkout and no-replace placement in the parent project.
func freshShelfHTTPUpload(t *testing.T, root, shelfRepo string, data []byte) (ipcserver.UploadChannelService, contract.ShelfItem) {
	t.Helper()
	clam, err := exec.LookPath("clamscan")
	if err != nil {
		t.Skip("real clamscan required")
	}
	owner, parent := uuid.NewString(), uuid.NewString()
	channels := &channel.Store{Root: filepath.Join(root, "channels"), Authority: shelfHTTPAuthority{owner: owner, parent: parent}, TokenKey: []byte(strings.Repeat("k", 32))}
	record, deliveries, err := channels.CreateUpload(uuid.NewString(), owner, manifest.Upload{OwnerRealm: owner, AuthorityRepoID: parent, UploadRepoID: shelfRepo, Slug: "delivery", Recipients: []string{"uploader@example.test"}, CollisionPolicy: manifest.CollisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := (authority.Resolver{Channels: channels}).InspectUpload(record.Alias, record.Manifest.Slug)
	if err != nil || projection.Validate() != nil {
		t.Fatalf("local projection: %+v %v validation=%v", projection, err, projection.Validate())
	}
	spool := intake.Store{Root: filepath.Join(root, "intake"), MaxBytes: 1 << 20}
	if err := os.MkdirAll(spool.Root, 0700); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(backchannel.Server{Authority: authority.Resolver{Channels: channels}})
	defer backend.Close()
	remote := backchannel.Client{BaseURL: backend.URL, HTTP: backend.Client()}
	if projection, err := remote.InspectUpload(record.Alias, record.Manifest.Slug); err != nil || projection.Validate() != nil {
		t.Fatalf("backchannel projection: %+v %v validation=%v", projection, err, projection.Validate())
	}
	server := httptest.NewServer(web.Handler{Backend: remote, Intake: &spool, VisitKey: []byte(strings.Repeat("v", 32))})
	defer server.Close()
	target := server.URL + "/" + record.Alias + "/" + record.Manifest.Slug + "?invite=" + url.QueryEscape(deliveries[0].Token)
	post := func(payload []byte) channel.UploadStatus {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if err := mw.WriteField("relative_path", "incoming/photo-1.jpg"); err != nil {
			t.Fatal(err)
		}
		part, err := mw.CreateFormFile("file", "photo-1.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err = mw.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 202 {
			t.Fatalf("HTTP upload: %d %v (body %d bytes)", resp.StatusCode, err, len(raw))
		}
		var receipt channel.UploadStatus
		if err = json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	received := post(data)
	if received.State != "pending" {
		t.Fatalf("accepted before AV/SVN: %+v", received)
	}
	db := filepath.Join(root, "lab.ndb")
	if err = os.WriteFile(db, []byte("FileES.Test.Reject:0:*:46494c4545532d544553542d52454a454354\n"), 0600); err != nil {
		t.Fatal(err)
	}
	look, err := exec.LookPath("svnlook")
	if err != nil {
		t.Fatal(err)
	}
	mucc, err := exec.LookPath("svnmucc")
	if err != nil {
		t.Fatal(err)
	}
	reaper := uploadworker.Reaper{Channels: channels, Intake: spool, ReposRoot: root, TrashRoot: filepath.Join(root, "trash"), Scanner: avscan.Command{Path: clam, Args: []string{"--database=" + db, "--no-summary", "--stdout"}}, Publisher: uploadworker.Publisher{SVNLook: look, SVNMucc: mucc}}
	if err = os.MkdirAll(reaper.TrashRoot, 0700); err != nil {
		t.Fatal(err)
	}
	summary, err := reaper.Reap(t.Context())
	if err != nil || summary.Accepted != 1 {
		t.Fatalf("AV/SVN: %+v %v", summary, err)
	}
	consumer := shelfHTTPConsumerAuthority{shelfDesktopAuthority: shelfDesktopAuthority{}, channels: channels, owner: owner, channelID: record.ChannelID}
	listing, err := consumer.ListShelf(t.Context(), "", "")
	if err != nil || len(listing.Items) != 1 || listing.Items[0].UploadID != received.UploadID || listing.Items[0].Revision != 1 {
		t.Fatalf("server index: %+v %v", listing, err)
	}
	resp, err := server.Client().Get(target + "&upload_status=" + url.QueryEscape(received.UploadID))
	if err != nil {
		t.Fatal(err)
	}
	var status channel.UploadStatus
	err = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if err != nil || status.State != "accepted" || status.SHA256 != listing.Items[0].SHA256 {
		t.Fatalf("uploader receipt: %+v %v", status, err)
	}
	// Retry of a fresh upload must not replace the accepted file or create r2.
	post([]byte("replacement"))
	summary, err = reaper.Reap(t.Context())
	if err != nil || summary.Failed != 1 {
		t.Fatalf("collision: %+v %v", summary, err)
	}
	head, err := exec.CommandContext(t.Context(), look, "youngest", filepath.Join(root, shelfRepo)).Output()
	if err != nil || strings.TrimSpace(string(head)) != "1" {
		t.Fatalf("collision changed HEAD: %s %v", head, err)
	}
	t.Log("fresh HTTP/backchannel, real AV/SVN, accepted index and no-overwrite PASS")
	return consumer, listing.Items[0]
}
