package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/pkg/repoworker"
	"filees/public-shares/authority"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/intake"
	"filees/public-shares/manifest"
	"filees/public-shares/recipientotp"
	"github.com/google/uuid"
)

type uploadStub struct {
	projection channel.UploadProjection
}

func (s uploadStub) Enter(context.Context, string, string) (authority.Entry, error) {
	return authority.Entry{}, authority.ErrNotFound
}
func (s uploadStub) Inspect(string, string) (channel.Projection, error) {
	return channel.Projection{}, authority.ErrNotFound
}
func (s uploadStub) InspectUpload(alias, slug string) (channel.UploadProjection, error) {
	if s.projection.Alias != alias || s.projection.Slug != slug {
		return channel.UploadProjection{}, authority.ErrNotFound
	}
	return s.projection, nil
}
func (s uploadStub) Check(context.Context, authority.ObjectRequest) (authority.ObjectPermit, error) {
	return authority.ObjectPermit{}, authority.ErrNotFound
}
func (s uploadStub) Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error) {
	return authority.FetchedLeaf{}, authority.ErrNotFound
}
func (s uploadStub) RequestRecipientOTP(context.Context, recipientotp.Request) error {
	return authority.ErrNotFound
}
func (s uploadStub) VerifyRecipientOTP(context.Context, recipientotp.VerifyRequest) (recipientotp.Grant, error) {
	return recipientotp.Grant{}, authority.ErrNotFound
}

func uploadHandler(t *testing.T, projection channel.UploadProjection) (Handler, *intake.Store) {
	t.Helper()
	store := &intake.Store{Root: t.TempDir(), MaxBytes: 1024, Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }}
	return Handler{Backend: uploadStub{projection: projection}, VisitKey: []byte(strings.Repeat("v", 32)), Intake: store, MaxUploadBytes: 1024}, store
}

func validUploadProjection(token string) channel.UploadProjection {
	return channel.UploadProjection{
		Schema: channel.UploadProjectionSchema, ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "oferta-a",
		State: channel.StateActive, Recipients: []channel.PublicRecipient{{InvitationHash: gate.TokenHash(token)}},
		Branding: realmbranding.Default(),
	}
}

func TestUploadGetRequiresInvitationAndHidesIdentity(t *testing.T) {
	token := "invite-token"
	handler, _ := uploadHandler(t, validUploadProjection(token))
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing invite status=%d", missing.Code)
	}
	wrong := httptest.NewRecorder()
	handler.ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite=nope", nil))
	if wrong.Code != http.StatusNotFound || missing.Body.String() != wrong.Body.String() {
		t.Fatal("invalid invite was distinguishable from missing")
	}
	ok := httptest.NewRecorder()
	handler.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite="+token, nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("status=%d", ok.Code)
	}
	body := ok.Body.String()
	if !strings.Contains(body, `type="file"`) || !strings.Contains(body, `name="file"`) || !strings.Contains(body, "Wyślij") {
		t.Fatalf("form missing:\n%s", body)
	}
	if !strings.Contains(body, `id="upload-form"`) || !strings.Contains(body, `id="upload-drop"`) || !strings.Contains(body, "Upuść plik") || !strings.Contains(body, "Wysyłanie pliku") || !strings.Contains(body, "Nie zamykaj karty") {
		t.Fatalf("drop field missing:\n%s", body)
	}
	csp := ok.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "script-src 'sha256-"+uploadFormScriptHash()+"'") {
		t.Fatalf("csp=%s", csp)
	}
	script := between(body, "<script>", "</script>")
	if script != uploadFormJS {
		t.Fatalf("rendered script diverged from CSP hash input:\n%s", script)
	}
	sum := sha256.Sum256([]byte(script))
	if !strings.Contains(csp, "sha256-"+base64.StdEncoding.EncodeToString(sum[:])) {
		t.Fatalf("csp hash mismatch: %s", csp)
	}
	for _, forbidden := range []string{"atmprojekt", "oferta-a", token, "invite-token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("form disclosed %q", forbidden)
		}
	}
}

func TestUploadPostStoresPayloadWithoutServingIt(t *testing.T) {
	token := "invite-token"
	handler, store := uploadHandler(t, validUploadProjection(token))
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "Opinia Łódź.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "wniesiony-plik"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/atmprojekt/oferta-a?invite="+token, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), "Plik został przyjęty") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "<script>") || strings.Contains(recorder.Header().Get("Content-Security-Policy"), "script-src") {
		t.Fatalf("accepted page kept the busy script: csp=%s", recorder.Header().Get("Content-Security-Policy"))
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
	payload, err := os.ReadFile(filepath.Join(store.Root, entries[0].Name(), "payload"))
	if err != nil || string(payload) != "wniesiony-plik" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	dirEntries, err := os.ReadDir(filepath.Join(store.Root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range dirEntries {
		if strings.Contains(strings.ToLower(entry.Name()), "opinia") || strings.Contains(entry.Name(), ".pdf") {
			t.Fatalf("filename leaked as %q", entry.Name())
		}
	}
	leak := httptest.NewRecorder()
	handler.ServeHTTP(leak, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a/"+entries[0].Name(), nil))
	if leak.Code != http.StatusNotFound {
		t.Fatalf("quarantine GET status=%d", leak.Code)
	}
}

func between(body, start, end string) string {
	from := strings.Index(body, start)
	if from < 0 {
		return ""
	}
	from += len(start)
	to := strings.Index(body[from:], end)
	if to < 0 {
		return ""
	}
	return body[from : from+to]
}

func TestUploadRequireOTPShowsNeutralGate(t *testing.T) {
	token := "invite-token"
	projection := validUploadProjection(token)
	projection.RequireOTP = true
	handler, store := uploadHandler(t, projection)
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing invite status=%d", missing.Code)
	}
	wrong := httptest.NewRecorder()
	handler.ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite=nope", nil))
	ok := httptest.NewRecorder()
	handler.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite="+token, nil))
	if wrong.Code != http.StatusOK || ok.Code != http.StatusOK || wrong.Body.String() != ok.Body.String() {
		t.Fatalf("otp gate distinguished invitations: wrong=%d ok=%d", wrong.Code, ok.Code)
	}
	body := ok.Body.String()
	if !strings.Contains(body, `name="email"`) || !strings.Contains(body, "Wyślij kod") || strings.Contains(body, `type="file"`) {
		t.Fatalf("otp gate missing:\n%s", body)
	}
	for _, forbidden := range []string{"atmprojekt", "oferta-a", token, "invite-token", "a@example"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("otp gate disclosed %q", forbidden)
		}
	}
	var fileBody bytes.Buffer
	writer := multipart.NewWriter(&fileBody)
	part, err := writer.CreateFormFile("file", "wniosek.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "za-wczesnie"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewRequest(http.MethodPost, "/atmprojekt/oferta-a?invite="+token, &fileBody)
	denied.Header.Set("Content-Type", writer.FormDataContentType())
	deniedRec := httptest.NewRecorder()
	handler.ServeHTTP(deniedRec, denied)
	if deniedRec.Code != http.StatusNotFound {
		t.Fatalf("file without grant status=%d", deniedRec.Code)
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("quarantine filled before OTP: %v %v", entries, err)
	}
}

type uploadOTPFixture struct {
	handler    Handler
	store      *channel.Store
	intake     *intake.Store
	invitation string
	now        *time.Time
}

func newUploadOTPWebFixture(t *testing.T) uploadOTPFixture {
	t.Helper()
	owner, authorityRepo, uploadRepo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Unix(1_700_000_000, 0).UTC()
	clock := &now
	channels := &channel.Store{
		Root: t.TempDir(), Authority: &webAuthority{owner: owner, repo: authorityRepo, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return *clock },
	}
	_, deliveries, err := channels.CreateUpload(uuid.NewString(), owner, manifest.Upload{
		OwnerRealm: owner, AuthorityRepoID: authorityRepo, UploadRepoID: uploadRepo,
		Slug: "oferta-a", Recipients: []string{"a@example.com"}, RequireOTP: true,
		CollisionPolicy: manifest.CollisionDeny,
	})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("create upload: %v deliveries=%v", err, deliveries)
	}
	otp := &recipientotp.Service{
		Root: t.TempDir(), Key: []byte(strings.Repeat("o", 32)), Channels: channels,
		Outbox: repoworker.PublicShareOutbox{Root: t.TempDir(), Now: func() time.Time { return *clock }},
		Now:    func() time.Time { return *clock },
	}
	resolver := authority.Resolver{Channels: channels, RecipientOTP: otp}
	intakeStore := &intake.Store{Root: t.TempDir(), MaxBytes: 1024, Now: func() time.Time { return *clock }}
	handler := Handler{Backend: resolver, VisitKey: []byte(strings.Repeat("v", 32)), Intake: intakeStore, MaxUploadBytes: 1024, Now: func() time.Time { return *clock }}
	return uploadOTPFixture{handler: handler, store: channels, intake: intakeStore, invitation: deliveries[0].Token, now: clock}
}

func TestUploadOTPExchangesTypedAddressThenAcceptsFile(t *testing.T) {
	f := newUploadOTPWebFixture(t)
	target := "/atmprojekt/oferta-a?invite=" + url.QueryEscape(f.invitation)
	sent := httptest.NewRequest(http.MethodPost, target, strings.NewReader("action=send&email="+url.QueryEscape("A@example.com")))
	sent.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sentRec := httptest.NewRecorder()
	f.handler.ServeHTTP(sentRec, sent)
	if sentRec.Code != http.StatusOK || !strings.Contains(sentRec.Body.String(), "Kod dostępu") {
		t.Fatalf("send status=%d body=%s", sentRec.Code, sentRec.Body.String())
	}
	service := f.handler.Backend.(authority.Resolver).RecipientOTP
	job, ok, err := service.Outbox.Claim(*f.now, time.Minute)
	if err != nil || !ok || job.Code == "" {
		t.Fatalf("otp mail=%+v %v %v", job, ok, err)
	}
	wrong := httptest.NewRequest(http.MethodPost, target, strings.NewReader("action=verify&email="+url.QueryEscape("A@example.com")+"&code=00000000"))
	wrong.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongRec := httptest.NewRecorder()
	f.handler.ServeHTTP(wrongRec, wrong)
	if wrongRec.Code != http.StatusOK || !strings.Contains(wrongRec.Body.String(), "nieprawidłowy") {
		t.Fatalf("wrong code status=%d body=%s", wrongRec.Code, wrongRec.Body.String())
	}
	verified := httptest.NewRequest(http.MethodPost, target, strings.NewReader("action=verify&email="+url.QueryEscape("A@example.com")+"&code="+job.Code))
	verified.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	verifiedRec := httptest.NewRecorder()
	f.handler.ServeHTTP(verifiedRec, verified)
	if verifiedRec.Code != http.StatusSeeOther {
		t.Fatalf("verify status=%d body=%s", verifiedRec.Code, verifiedRec.Body.String())
	}
	location, err := url.Parse(verifiedRec.Header().Get("Location"))
	if err != nil || location.Query().Get("g") == "" || location.Query().Get("invite") != f.invitation {
		t.Fatalf("grant redirect=%s err=%v", verifiedRec.Header().Get("Location"), err)
	}
	granted := httptest.NewRecorder()
	f.handler.ServeHTTP(granted, httptest.NewRequest(http.MethodGet, location.String(), nil))
	if granted.Code != http.StatusOK || !strings.Contains(granted.Body.String(), `type="file"`) {
		t.Fatalf("granted form status=%d body=%s", granted.Code, granted.Body.String())
	}
	if strings.Contains(granted.Body.String(), f.invitation) {
		t.Fatal("file form disclosed invitation")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "Opinia.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "wniesiony-plik"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, location.String(), &body)
	post.Header.Set("Content-Type", writer.FormDataContentType())
	accepted := httptest.NewRecorder()
	f.handler.ServeHTTP(accepted, post)
	if accepted.Code != http.StatusAccepted || !strings.Contains(accepted.Body.String(), "Plik został przyjęty") {
		t.Fatalf("accept status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	entries, err := os.ReadDir(f.intake.Root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
}
