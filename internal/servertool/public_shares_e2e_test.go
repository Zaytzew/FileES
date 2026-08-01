package servertool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
	"filees/public-shares/authority"
	"filees/public-shares/backchannel"
	"filees/public-shares/cache"
	"filees/public-shares/channel"
	"filees/public-shares/web"
	"github.com/google/uuid"
)

// TestPublicSharesDestructiveE2E exercises the complete open/closed read path
// against real FSFS repositories and the real repository worker. Everything is
// rooted in t.TempDir; no installed service or VM filesystem is touched.
func TestPublicSharesDestructiveE2E(t *testing.T) {
	f := newRealmRemovalE2EFixture(t, 0)
	owner, guest := f.targetRealm, f.otherRealm
	repository := f.ownedRepos[0]
	aliases := repoworker.RealmAliases{ServiceWC: f.activationConfig.ServiceWorkingCopy, Runner: f.publisher.Runner}
	if _, err := aliases.Claim(context.Background(), owner, "atmprojekt"); err != nil {
		t.Fatal(err)
	}
	if _, err := aliases.Claim(context.Background(), guest, "guestrealm"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.publisher.Grant(context.Background(), owner, guest, repository.RepoID, "rw"); err != nil {
		t.Fatal(err)
	}

	content := "PUBLIC-SHARE-E2E-CONTENT"
	source := filepath.Join(f.root, "project.pdf")
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(f.repositoriesRoot, repository.RepoID)
	realmRemovalE2ERun(t, f.svn, "import", "--non-interactive", "--no-auth-cache", "-m", "public share leaf", source, "file://"+repoPath+"/data/project.pdf")
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	svnlook, _ = filepath.Abs(svnlook)
	headBefore := youngestRevision(t, svnlook, repoPath)

	stateRoot := filepath.Join(f.resultsRoot, "public-shares")
	channels := &channel.Store{Root: stateRoot, Authority: f.publisher, TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return f.now }}
	outbox := repoworker.PublicShareOutbox{Root: filepath.Join(stateRoot, "outbox"), Now: func() time.Time { return f.now }}
	resultStore, err := repoworker.NewFileStore(filepath.Join(f.resultsRoot, "public-share-results"))
	if err != nil {
		t.Fatal(err)
	}
	worker := &repoworker.Worker{Store: resultStore, PublicShares: repoworker.ChannelPublicShareService{Channels: channels, Deliverer: outbox}, Now: func() time.Time { return f.now }}
	ownerSession := repoworker.Session{ClientID: f.targetClients[0].ClientID, RealmID: owner, CanCreateRepositories: true}
	guestSession := repoworker.Session{ClientID: f.otherClient.ClientID, RealmID: guest, CanCreateRepositories: true}

	declaration := func(slug string, recipients []string) control.PublicShareDeclaration {
		return control.PublicShareDeclaration{RepoID: repository.RepoID, SourceRoot: "data", Slug: slug, Recipients: recipients, Objects: []control.PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "data/project.pdf", DisplayName: "Projekt.pdf"}}}
	}
	issue := func(session repoworker.Session, typ control.TicketType, payload any) (string, control.Result) {
		operationID := uuid.NewString()
		ticket, err := control.NewTicket(operationID, uuid.NewString(), typ, session.ClientID, payload, f.now)
		if err != nil {
			t.Fatal(err)
		}
		result, err := worker.Handle(context.Background(), session, ticket)
		if err != nil {
			t.Fatal(err)
		}
		return operationID, result
	}
	openID, openResult := issue(ownerSession, control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: declaration("otwarty-przetarg", nil)})
	if openResult.Status != control.ResultOK {
		t.Fatalf("open create=%+v", openResult)
	}
	closedID, closedResult := issue(ownerSession, control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: declaration("zamkniety-przetarg", []string{"recipient@example.com"})})
	if closedResult.Status != control.ResultOK {
		t.Fatalf("closed create=%+v", closedResult)
	}
	_, guestResult := issue(guestSession, control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: declaration("guest-cannot-publish", nil)})
	if guestResult.Status != control.ResultError || guestResult.Error.Code != "PUBLIC_SHARE_REJECTED" {
		t.Fatalf("rw guest published owner data: %+v", guestResult)
	}

	resolver := authority.Resolver{Channels: channels, Source: authority.SVNLookSource{SVNLook: svnlook, RepositoriesRoot: f.repositoriesRoot}, FrostKey: []byte(strings.Repeat("f", 32)), StagingRoot: filepath.Join(f.root, "authority-staging"), MaxLeafSize: 1 << 20}
	backServer := backchannel.Server{Authority: resolver}
	backClient := backchannel.Client{BaseURL: "http://authority", HTTP: &http.Client{Transport: e2eHandlerTransport{handler: backServer}}}
	handler := web.Handler{Backend: backClient, Cache: &cache.Store{Config: cache.Config{Root: filepath.Join(f.root, "cache"), TTL: 12 * time.Hour, MaxSize: 1 << 20}}, VisitKey: []byte(strings.Repeat("v", 32)), Now: func() time.Time { return f.now }}

	openVisit := e2eVisit(t, handler, "/atmprojekt/otwarty-przetarg")
	listing := e2eRequest(handler, http.MethodGet, "/atmprojekt/otwarty-przetarg?v="+url.QueryEscape(openVisit), nil)
	if listing.Code != http.StatusOK || strings.Contains(listing.Body.String(), "data/project.pdf") {
		t.Fatalf("open listing=%d %s", listing.Code, listing.Body.String())
	}
	get := e2eRequest(handler, http.MethodGet, "/atmprojekt/otwarty-przetarg/get/7f3a1c9e2b4d6a80?v="+url.QueryEscape(openVisit), nil)
	if get.Code != http.StatusSeeOther {
		t.Fatalf("open prepare=%d %s", get.Code, get.Body.String())
	}
	file := e2eRequest(handler, http.MethodGet, "/atmprojekt/otwarty-przetarg/file/7f3a1c9e2b4d6a80?v="+url.QueryEscape(openVisit), map[string]string{"Range": "bytes=7-11"})
	if file.Code != http.StatusPartialContent || file.Body.String() != "SHARE" {
		t.Fatalf("range=%d %q", file.Code, file.Body.String())
	}

	job, ok, err := outbox.Claim(f.now, time.Minute)
	if err != nil || !ok || job.ChannelID != closedID {
		t.Fatalf("closed token job=%+v %v %v", job, ok, err)
	}
	if err := outbox.MarkFailed(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	wrong := e2eRequest(handler, http.MethodGet, "/atmprojekt/zamkniety-przetarg?token=wrong", nil)
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong closed token=%d", wrong.Code)
	}
	closedVisit := e2eVisit(t, handler, "/atmprojekt/zamkniety-przetarg?token="+url.QueryEscape(job.Token))
	closedGet := e2eRequest(handler, http.MethodGet, "/atmprojekt/zamkniety-przetarg/get/7f3a1c9e2b4d6a80?v="+url.QueryEscape(closedVisit), nil)
	if closedGet.Code != http.StatusSeeOther {
		t.Fatalf("closed prepare=%d", closedGet.Code)
	}
	_, revoked := issue(ownerSession, control.TicketRevokePublicShare, control.RevokePublicSharePayload{ChannelID: closedID})
	if revoked.Status != control.ResultOK {
		t.Fatalf("revoke=%+v", revoked)
	}
	closedFile := e2eRequest(handler, http.MethodGet, "/atmprojekt/zamkniety-przetarg/file/7f3a1c9e2b4d6a80?v="+url.QueryEscape(closedVisit), nil)
	if closedFile.Code != http.StatusNotFound {
		t.Fatalf("cached closed file survived revoke=%d", closedFile.Code)
	}
	if youngest := youngestRevision(t, svnlook, repoPath); youngest != headBefore {
		t.Fatalf("public reads wrote repository: head %d -> %d", headBefore, youngest)
	}
	if openID == "" {
		t.Fatal("open channel id was not bound to operation")
	}
}

type e2eHandlerTransport struct{ handler http.Handler }

func (t e2eHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func e2eRequest(handler http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://get.example.test"+target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func e2eVisit(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	response := e2eRequest(handler, http.MethodGet, target, nil)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("entry %s=%d %s", target, response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	visit := location.Query().Get("v")
	if visit == "" {
		t.Fatalf("entry has no visit: %s", location)
	}
	return visit
}

func youngestRevision(t *testing.T, svnlook, repository string) int64 {
	t.Helper()
	raw, err := exec.Command(svnlook, "youngest", repository).Output()
	if err != nil {
		t.Fatal(err)
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}
