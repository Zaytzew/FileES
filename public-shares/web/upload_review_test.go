//go:build !windows

package web

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
	"sync"
	"testing"
	"time"

	"filees/internal/uploadworker"
	"filees/pkg/avscan"
	"filees/public-shares/authority"
	"filees/public-shares/backchannel"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/intake"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type reviewFixture struct {
	handler                                  Handler
	channels                                 *channel.Store
	intake                                   intake.Store
	reaper                                   uploadworker.Reaper
	token, otherToken, channelID, repository string
}

func reviewSetup(t *testing.T) reviewFixture {
	t.Helper()
	svnadmin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Skip("svnadmin unavailable")
	}
	svnlook, e := exec.LookPath("svnlook")
	if e != nil {
		t.Skip("svnlook unavailable")
	}
	svnmucc, e := exec.LookPath("svnmucc")
	if e != nil {
		t.Skip("svnmucc unavailable")
	}
	owner, parent, repo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	channels := &channel.Store{Root: t.TempDir(), Authority: &webAuthority{owner: owner, repo: parent, alias: "example"}, TokenKey: []byte(strings.Repeat("k", 32))}
	record, deliveries, e := channels.CreateUpload(uuid.NewString(), owner, manifest.Upload{OwnerRealm: owner, AuthorityRepoID: parent, UploadRepoID: repo, Slug: "delivery", Recipients: []string{"a@example.com", "b@example.com"}, CollisionPolicy: manifest.CollisionDeny})
	if e != nil || len(deliveries) != 2 {
		t.Fatal(record, deliveries, e)
	}
	repos := t.TempDir()
	repository := filepath.Join(repos, repo)
	if out, e := exec.Command(svnadmin, "create", repository).CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	spool := intake.Store{Root: t.TempDir(), MaxBytes: 1 << 20}
	resolver := authority.Resolver{Channels: channels}
	// Exercise the real HTTP backchannel as well as the public handler.
	server := httptest.NewServer(backchannel.Server{Authority: resolver})
	t.Cleanup(server.Close)
	h := Handler{Backend: backchannel.Client{BaseURL: server.URL, HTTP: server.Client()}, Intake: &spool, VisitKey: []byte(strings.Repeat("v", 32))}
	reaper := uploadworker.Reaper{Channels: channels, Intake: spool, ReposRoot: repos, TrashRoot: t.TempDir(), Scanner: avscan.Static{Verdict: avscan.Clean}, Publisher: uploadworker.Publisher{SVNLook: svnlook, SVNMucc: svnmucc}}
	if e := os.Chmod(reaper.TrashRoot, 0700); e != nil {
		t.Fatal(e)
	}
	return reviewFixture{handler: h, channels: channels, intake: spool, reaper: reaper, token: deliveries[0].Token, otherToken: deliveries[1].Token, channelID: record.ChannelID, repository: repository}
}

func (f reviewFixture) target(token string) string {
	return "/example/delivery?invite=" + url.QueryEscape(token)
}
func (f reviewFixture) post(t *testing.T, name, path string) channel.UploadStatus {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if path != "" {
		if e := mw.WriteField("relative_path", path); e != nil {
			t.Fatal(e)
		}
	}
	p, e := mw.CreateFormFile("file", name)
	if e != nil {
		t.Fatal(e)
	}
	io.WriteString(p, "payload")
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, f.target(f.token), &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result channel.UploadStatus
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return result
}
func (f reviewFixture) status(t *testing.T, token, id string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, f.target(token)+"&upload_status="+url.QueryEscape(id), nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestUploadReviewReceiptRequiresActualSVNCommitAndOwnInvitation(t *testing.T) {
	f := reviewSetup(t)
	received := f.post(t, "Opinia Łódź.pdf", "Oferta 1/Rysunki/Opinia Łódź.pdf")
	if received.State != "pending" || received.Size != 7 || received.RepoPath != "" {
		t.Fatal(received)
	}
	before := f.status(t, f.token, received.UploadID)
	if before.Code != 200 || !strings.Contains(before.Body.String(), `"state":"pending"`) {
		t.Fatal(before.Code, before.Body.String())
	}
	summary, e := f.reaper.Reap(context.Background())
	if e != nil || summary.Accepted != 1 {
		t.Fatal(summary, e)
	}
	after := f.status(t, f.token, received.UploadID)
	var result channel.UploadStatus
	if e := json.Unmarshal(after.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if after.Code != 200 || result.State != "accepted" || result.RepoPath != "Oferta-1/Rysunki/Opinia-Lodz.pdf" || result.Revision != 1 || result.SHA256 != received.SHA256 {
		t.Fatal(after.Code, result)
	}
	if out, e := exec.Command(f.reaper.Publisher.SVNLook, "cat", f.repository, result.RepoPath).CombinedOutput(); e != nil || string(out) != "payload" {
		t.Fatal(e, string(out))
	}
	if w := f.status(t, f.otherToken, received.UploadID); w.Code != 404 {
		t.Fatal("another contributor read receipt", w.Code, w.Body.String())
	}
	if w := f.status(t, "wrong", received.UploadID); w.Code != 404 {
		t.Fatal("wrong invitation", w.Code)
	}
	// Collision does not overwrite or produce another revision.
	collision := f.post(t, "Opinia Łódź.pdf", "Oferta 1/Rysunki/Opinia Łódź.pdf")
	summary, e = f.reaper.Reap(context.Background())
	if e != nil || summary.Failed != 1 {
		t.Fatal(summary, e)
	}
	w := f.status(t, f.token, collision.UploadID)
	if !strings.Contains(w.Body.String(), `"reason":"collision"`) || strings.Contains(w.Body.String(), f.repository) {
		t.Fatal(w.Body.String())
	}
	if out, e := exec.Command(f.reaper.Publisher.SVNLook, "youngest", f.repository).CombinedOutput(); e != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatal(e, string(out))
	}
	record, e := f.channels.GetUpload(f.channelID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.channels.RevokeUpload(record.OwnerRealm, f.channelID); e != nil {
		t.Fatal(e)
	}
	if w := f.status(t, f.token, received.UploadID); w.Code != 404 {
		t.Fatal("revoked invitation still reads receipt", w.Code)
	}
}

func TestUploadReviewAVRejectionIsVisibleAndNeverAccepted(t *testing.T) {
	f := reviewSetup(t)
	record, _ := f.channels.GetUpload(f.channelID)
	trashRepo := filepath.Join(f.reaper.ReposRoot, channel.TrashRepositoryID(record.OwnerRealm))
	svnadmin, _ := exec.LookPath("svnadmin")
	if out, e := exec.Command(svnadmin, "create", trashRepo).CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	received := f.post(t, "bad.txt", "")
	f.reaper.Scanner = avscan.Static{Verdict: avscan.Infected, Detail: "private scanner signature"}
	summary, e := f.reaper.Reap(context.Background())
	if e != nil || summary.Rejected != 1 {
		t.Fatal(summary, e)
	}
	w := f.status(t, f.token, received.UploadID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"rejected"`) || strings.Contains(w.Body.String(), "private scanner") {
		t.Fatal(w.Code, w.Body.String())
	}
	if out, e := exec.Command(f.reaper.Publisher.SVNLook, "youngest", f.repository).CombinedOutput(); e != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatal(e, string(out))
	}
}

func TestUploadReviewRejectsUnsafeRelativePathBeforeSpooling(t *testing.T) {
	for _, path := range []string{"../a.txt", "/a.txt", ".svn/a.txt", "Folder//a.txt", "Folder/b.txt"} {
		t.Run(path, func(t *testing.T) {
			h, s := uploadHandler(t, validUploadProjection("token"))
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			mw.WriteField("relative_path", path)
			p, _ := mw.CreateFormFile("file", "a.txt")
			p.Write([]byte("x"))
			mw.Close()
			r := httptest.NewRequest(http.MethodPost, "/atmprojekt/oferta-a?invite=token", &body)
			r.Header.Set("Content-Type", mw.FormDataContentType())
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatal(w.Code)
			}
			entries, e := os.ReadDir(s.Root)
			if e != nil || len(entries) != 0 {
				t.Fatal(entries, e)
			}
		})
	}
}

// This opt-in fixture is for real WebKit/WebDriver acceptance. It never uses
// owner data or credentials. The Python driver ends the session explicitly.
func TestUploadReviewBrowserFixture(t *testing.T) {
	output := os.Getenv("FILEES_UPLOAD_REVIEW_FIXTURE")
	if output == "" {
		t.Skip("interactive browser fixture")
	}
	f := reviewSetup(t)
	done := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__reap":
			result, e := f.reaper.Reap(r.Context())
			if e != nil {
				http.Error(w, "reap failed", 500)
				return
			}
			json.NewEncoder(w).Encode(result)
		case "/__state":
			ready, _ := f.intake.ListReady()
			out, _ := exec.Command(f.reaper.Publisher.SVNLook, "youngest", f.repository).Output()
			json.NewEncoder(w).Encode(map[string]any{"ready": len(ready), "revision": strings.TrimSpace(string(out))})
		case "/__finish":
			once.Do(func() { close(done) })
			fmt.Fprint(w, "done")
		default:
			f.handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"url": server.URL + f.target(f.token), "base": server.URL})
	if e := os.WriteFile(output, raw, 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(12 * time.Minute):
		t.Fatal("browser fixture timeout")
	}
}

func TestUploadReviewStatusStillRequiresOTP(t *testing.T) {
	f := newUploadOTPWebFixture(t)
	id := uuid.NewString()
	projection, e := f.handler.Backend.InspectUpload("atmprojekt", "oferta-a")
	if e != nil {
		t.Fatal(e)
	}
	if e := f.store.RecordAccepted(channel.Accepted{ChannelID: projection.ChannelID, UploadID: id, TokenSHA256: gate.TokenHash(f.invitation), RepoPath: "a.txt"}); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite="+url.QueryEscape(f.invitation)+"&upload_status="+id, nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), "a.txt") {
		t.Fatal(w.Code, w.Body.String())
	}
}
