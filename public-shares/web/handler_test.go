package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/public-shares/authority"
	"filees/public-shares/cache"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type webAuthority struct{ owner, repo, alias string }

func (a *webAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a *webAuthority) ActiveRealmAlias(owner string) (string, error) {
	if owner != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}

type webSource struct {
	head   int64
	values map[int64]string
}

func (s *webSource) Head(context.Context, string) (int64, error) { return s.head, nil }
func (s *webSource) Cat(_ context.Context, _, _ string, revision int64, dst io.Writer) error {
	value, ok := s.values[revision]
	if !ok {
		return errors.New("not found")
	}
	_, err := io.WriteString(dst, value)
	return err
}

type webFixture struct {
	handler          Handler
	store            *channel.Store
	share            manifest.Share
	owner, channelID string
	deliveries       []channel.Delivery
}

func newWebFixture(t *testing.T, configure func(*manifest.Share)) webFixture {
	t.Helper()
	owner, repo := uuid.NewString(), uuid.NewString()
	clock := time.Unix(1700000000, 0).UTC()
	store := &channel.Store{Root: t.TempDir(), Authority: &webAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return clock }}
	share := manifest.Share{OwnerRealm: owner, RepoID: repo, SourceRoot: "wydanie", Slug: "przetarg-2026", Objects: []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt budowlany.pdf"}}}
	if configure != nil {
		configure(&share)
	}
	channelID := uuid.NewString()
	_, deliveries, err := store.Create(channelID, owner, share)
	if err != nil {
		t.Fatal(err)
	}
	resolver := authority.Resolver{Channels: store, Source: &webSource{head: 5, values: map[int64]string{5: "revision five payload"}}, FrostKey: []byte(strings.Repeat("f", 32)), StagingRoot: t.TempDir(), MaxLeafSize: 1 << 20}
	cacheStore := &cache.Store{Config: cache.Config{Root: t.TempDir(), TTL: 12 * time.Hour, MaxSize: 1024 * 1024}}
	handler := Handler{Backend: resolver, Cache: cacheStore, VisitKey: []byte(strings.Repeat("v", 32)), Now: func() time.Time { return clock }}
	return webFixture{handler: handler, store: store, share: share, owner: owner, channelID: channelID, deliveries: deliveries}
}

func perform(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func visitFromRedirect(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	value := location.Query().Get("v")
	if value == "" {
		t.Fatalf("redirect has no visit: %s", location)
	}
	return value
}

func TestOpenShareListingCacheAndRange(t *testing.T) {
	f := newWebFixture(t, nil)
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
	listing := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Projekt budowlany.pdf") || strings.Contains(listing.Body.String(), "wydanie/projekt.pdf") {
		t.Fatalf("listing status=%d body=%s", listing.Code, listing.Body.String())
	}
	if listing.Header().Get("Content-Security-Policy") == "" || listing.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers missing: %v", listing.Header())
	}
	getURL := "https://example.test/atmprojekt/przetarg-2026/get/7f3a1c9e2b4d6a80?v=" + url.QueryEscape(visit)
	if prepared := perform(f.handler, http.MethodGet, getURL, "", nil); prepared.Code != http.StatusSeeOther {
		t.Fatalf("prepare status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	fileURL := "https://example.test/atmprojekt/przetarg-2026/file/7f3a1c9e2b4d6a80?v=" + url.QueryEscape(visit)
	response := perform(f.handler, http.MethodGet, fileURL, "", map[string]string{"Range": "bytes=9-12"})
	if response.Code != http.StatusPartialContent || response.Body.String() != "five" {
		t.Fatalf("range status=%d body=%q headers=%v", response.Code, response.Body.String(), response.Header())
	}
	if response.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("unsafe attachment headers: %v", response.Header())
	}
}

func TestClosedTokenIsExchangedAndRevokeIsImmediate(t *testing.T) {
	f := newWebFixture(t, func(share *manifest.Share) { share.Recipients = []string{"a@example.com"} })
	if wrong := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?token=wrong", "", nil); wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong token status=%d", wrong.Code)
	}
	token := f.deliveries[0].Token
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?token="+url.QueryEscape(token), "", nil))
	if strings.Contains(visit, token) {
		t.Fatal("recipient bearer token leaked into visit capability")
	}
	getURL := "https://example.test/atmprojekt/przetarg-2026/get/7f3a1c9e2b4d6a80?v=" + url.QueryEscape(visit)
	if got := perform(f.handler, http.MethodGet, getURL, "", nil); got.Code != http.StatusSeeOther {
		t.Fatalf("prepare=%d", got.Code)
	}
	if _, err := f.store.Revoke(f.owner, f.channelID); err != nil {
		t.Fatal(err)
	}
	fileURL := "https://example.test/atmprojekt/przetarg-2026/file/7f3a1c9e2b4d6a80?v=" + url.QueryEscape(visit)
	if got := perform(f.handler, http.MethodGet, fileURL, "", nil); got.Code != http.StatusNotFound {
		t.Fatalf("cached content survived revoke: status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestPasswordNeverAppearsInVisitURL(t *testing.T) {
	verifier, err := gate.HashPassword("sekretne haslo", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newWebFixture(t, func(share *manifest.Share) { share.Password = verifier })
	form := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil)
	if form.Code != http.StatusOK || !strings.Contains(form.Body.String(), `type="password"`) {
		t.Fatalf("form=%d %s", form.Code, form.Body.String())
	}
	wrong := perform(f.handler, http.MethodPost, "https://example.test/atmprojekt/przetarg-2026", "password=zle-haslo", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong password=%d", wrong.Code)
	}
	correct := perform(f.handler, http.MethodPost, "https://example.test/atmprojekt/przetarg-2026", "password="+url.QueryEscape("sekretne haslo"), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	visit := visitFromRedirect(t, correct)
	if strings.Contains(correct.Header().Get("Location"), "sekretne") || strings.Contains(visit, "sekretne") {
		t.Fatal("password leaked into redirect")
	}
}

func TestPasswordVerificationHasHardMemoryConcurrencyBound(t *testing.T) {
	verifier, err := gate.HashPassword("sekretne haslo", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newWebFixture(t, func(share *manifest.Share) { share.Password = verifier })
	passwordCheckSlot <- struct{}{}
	blocked := perform(f.handler, http.MethodPost, "https://example.test/atmprojekt/przetarg-2026", "password="+url.QueryEscape("sekretne haslo"), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	<-passwordCheckSlot
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("concurrent password check status=%d", blocked.Code)
	}
}

func TestFetchCoordinatorCoalescesOneOpaqueLeaf(t *testing.T) {
	coordinator := &FetchCoordinator{}
	start, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if err := coordinator.Do(context.Background(), strings.Repeat("a", 64), func() error {
				calls.Add(1)
				<-release
				return nil
			}); err != nil {
				t.Errorf("coordinated fetch: %v", err)
			}
		}()
	}
	close(start)
	time.Sleep(20 * time.Millisecond)
	close(release)
	group.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("backend fetch calls=%d, want 1", got)
	}
}
