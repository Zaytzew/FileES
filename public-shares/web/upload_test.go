package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/authority"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/intake"
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

func TestUploadRequireOTPIsFailClosed(t *testing.T) {
	token := "invite-token"
	projection := validUploadProjection(token)
	projection.RequireOTP = true
	handler, _ := uploadHandler(t, projection)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite="+token, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("otp channel status=%d", recorder.Code)
	}
}
