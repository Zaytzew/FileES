package web

import (
	"context"
	"encoding/base64"
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

	"filees/pkg/realmbranding"
	"filees/public-shares/authority"
	"filees/public-shares/cache"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func TestListingAppliesOnlyRealmLogoAndLeadingColor(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	branding, err := realmbranding.FromBytes("#008C45", "image/png", png)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	Handler{}.renderListing(recorder, channel.Projection{Alias: "acme", Slug: "zegrze", Branding: branding}, visit{Revision: 1}, "v", false)
	body := recorder.Body.String()
	if !strings.Contains(body, "--owner-accent:#008C45;--owner-ink:#008C45") || !strings.Contains(body, `class="owner-logo"`) || !strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "object-fit:contain") || !strings.Contains(body, "h1{color:var(--owner-ink)") {
		t.Fatalf("owner branding was not rendered safely:\n%s", body)
	}
	if strings.Contains(body, "image/svg") || strings.Contains(recorder.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatalf("unsafe branding surface rendered: %s", recorder.Header().Get("Content-Security-Policy"))
	}
}

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
func (a *webAuthority) ActiveRealmBranding(owner string) (realmbranding.Branding, error) {
	if owner != a.owner {
		return realmbranding.Branding{}, errors.New("not owner")
	}
	return realmbranding.Default(), nil
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

func TestListingBuildsEscapedExplorerTreeWithOptionalSizes(t *testing.T) {
	size := int64(1536)
	projection := channel.Projection{
		Alias: "acme", Slug: "rysunki", Objects: []channel.PublicObject{
			{PublicID: "1234567890abcdef", DisplayName: "Branża/Rysunki/<script>.pdf", Size: &size},
			{PublicID: "fedcba0987654321", DisplayName: "README", Size: nil},
		},
	}
	recorder := httptest.NewRecorder()
	securityHeaders(recorder)
	Handler{Cache: &cache.Store{}, MaxBundleFiles: 10, MaxBundleSize: 1 << 20}.renderListing(recorder, projection, visit{Revision: 17}, "visit-token", false)
	body := recorder.Body.String()
	for _, expected := range []string{`class="directory"`, "Branża", "Rysunki", "&lt;script&gt;.pdf", "Dokument PDF", "1.5 KB", "README", ">—<", "/acme/rysunki/get/1234567890abcdef?v=visit-token"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("listing lacks %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "<script src") {
		t.Fatalf("display name was not escaped:\n%s", body)
	}
	if strings.Contains(body, `<details class="directory" open>`) || !strings.Contains(body, `<svg`) || !strings.Contains(body, `name="object"`) || !strings.Contains(body, "Pobierz całość") {
		t.Fatalf("listing does not use collapsed MIME-icon bundle UI:\n%s", body)
	}
	csp := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'sha256-") || !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("listing CSP does not bind the embedded stylesheet: %s", csp)
	}
}

func TestListingTreeSortsDirectoriesAndFiles(t *testing.T) {
	projection := channel.Projection{Alias: "acme", Slug: "sort", Objects: []channel.PublicObject{
		{PublicID: "1111111111111111", DisplayName: "zeta/z.txt"},
		{PublicID: "2222222222222222", DisplayName: "Alfa/b.txt"},
		{PublicID: "3333333333333333", DisplayName: "Alfa/A.txt"},
		{PublicID: "4444444444444444", DisplayName: "Alfa/Rysunek 10.pdf"},
		{PublicID: "5555555555555555", DisplayName: "Alfa/Rysunek 2.pdf"},
	}}
	tree := buildListingTree(projection, "v", false)
	if len(tree.Directories) != 2 || tree.Directories[0].Name != "Alfa" || tree.Directories[1].Name != "zeta" {
		t.Fatalf("directories=%+v", tree.Directories)
	}
	if len(tree.Directories[0].Files) != 4 || tree.Directories[0].Files[0].Name != "A.txt" || tree.Directories[0].Files[1].Name != "b.txt" || tree.Directories[0].Files[2].Name != "Rysunek 2.pdf" || tree.Directories[0].Files[3].Name != "Rysunek 10.pdf" {
		t.Fatalf("files=%+v", tree.Directories[0].Files)
	}
}

func TestBundleSelectionUsesOnlyPublicProjectionAndSafeNames(t *testing.T) {
	projection := channel.Projection{Alias: "acme", Slug: "Zegrze: komplet", Objects: []channel.PublicObject{
		{PublicID: "1111111111111111", DisplayName: "Dokumenty/plan?.pdf"},
		{PublicID: "2222222222222222", DisplayName: "Dokumenty/pod/plan?.pdf"},
		{PublicID: "3333333333333333", DisplayName: "Obrazy/widok.jpg"},
	}}
	objects, name, ok := selectBundleObjects(projection, url.Values{"folder": {"Dokumenty"}})
	if !ok || name != "Dokumenty" || len(objects) != 2 {
		t.Fatalf("folder bundle objects=%+v name=%q ok=%v", objects, name, ok)
	}
	objects, name, ok = selectBundleObjects(projection, url.Values{"object": {"3333333333333333"}})
	if !ok || name != "Zegrze_ komplet-wybrane" || len(objects) != 1 || objects[0].PublicID != "3333333333333333" {
		t.Fatalf("selection bundle objects=%+v name=%q ok=%v", objects, name, ok)
	}
	if _, _, ok = selectBundleObjects(projection, url.Values{"object": {"private-repo-path"}}); ok {
		t.Fatal("unknown public ID was accepted")
	}
	used := map[string]int{}
	first := uniqueArchiveName(safeArchivePath(`Dokumenty/..\\sekret?.pdf`), used)
	second := uniqueArchiveName(first, used)
	if strings.ContainsAny(first, `\\?`) || first == second || strings.HasPrefix(first, "/") {
		t.Fatalf("unsafe or duplicate archive names: %q %q", first, second)
	}
}

func TestEmptyListingExplainsPlaceholder(t *testing.T) {
	recorder := httptest.NewRecorder()
	Handler{}.renderListing(recorder, channel.Projection{Alias: "acme", Slug: "placeholder"}, visit{Revision: 3}, "v", false)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Ten folder jest jeszcze pusty") || strings.Contains(recorder.Body.String(), `class="columns"`) {
		t.Fatalf("empty listing status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestListingCountUsesPolishPluralForm(t *testing.T) {
	for count, want := range map[int]string{0: "0 plików", 1: "1 plik", 2: "2 pliki", 5: "5 plików", 12: "12 plików", 22: "22 pliki"} {
		if got := formatListingCount(count); got != want {
			t.Errorf("count %d = %q, want %q", count, got, want)
		}
	}
}
