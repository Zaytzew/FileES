package backchannel

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filees/public-shares/authority"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

type stubAuthority struct {
	projection channel.Projection
	body       string
}

func (s stubAuthority) Enter(context.Context, string, string) (authority.Entry, error) {
	return authority.Entry{Projection: s.projection, Revision: 7, FrostProof: "proof"}, nil
}
func (s stubAuthority) Inspect(string, string) (channel.Projection, error) { return s.projection, nil }
func (s stubAuthority) Check(context.Context, authority.ObjectRequest) (authority.ObjectPermit, error) {
	return authority.ObjectPermit{CacheKey: strings.Repeat("a", 64), DisplayName: "Projekt.pdf", Revision: 7}, nil
}
func (s stubAuthority) Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error) {
	return authority.FetchedLeaf{ObjectPermit: authority.ObjectPermit{CacheKey: strings.Repeat("a", 64), DisplayName: "Projekt.pdf", Revision: 7}, Size: int64(len(s.body)), MD5: strings.Repeat("b", 32), Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestClientServerContractAndStreamingFetch(t *testing.T) {
	projection := channel.Projection{Schema: channel.ProjectionSchema, ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "przetarg-2026", State: channel.StateActive, Objects: []channel.PublicObject{{PublicID: "7f3a1c9e2b4d6a80", DisplayName: "Projekt.pdf"}}}
	server := Server{Authority: stubAuthority{projection: projection, body: "payload"}}
	client := Client{BaseURL: "http://authority", HTTP: &http.Client{Transport: handlerTransport{handler: server}}}
	entry, err := client.Enter(context.Background(), projection.Alias, projection.Slug)
	if err != nil || entry.Revision != 7 || entry.Projection.ChannelID != projection.ChannelID {
		t.Fatalf("entry=%+v %v", entry, err)
	}
	request := authority.ObjectRequest{ChannelID: projection.ChannelID, PublicID: projection.Objects[0].PublicID, Revision: 7, FrostProof: "proof"}
	leaf, err := client.Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(leaf.Body)
	leaf.Body.Close()
	if string(raw) != "payload" || leaf.CacheKey != strings.Repeat("a", 64) || leaf.DisplayName != "Projekt.pdf" {
		t.Fatalf("leaf=%+v body=%q", leaf, raw)
	}
}

func TestMalformedOrWrongProtocolIs404(t *testing.T) {
	server := Server{Authority: stubAuthority{}}
	request := httptest.NewRequest(http.MethodPost, "http://authority/v1/entry", bytes.NewBufferString(`{"protocol":"v0","alias":"a","slug":"b"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServerRejectsImpreciseContentTypeAndOversizedJSON(t *testing.T) {
	server := Server{Authority: stubAuthority{}}
	for _, test := range []struct {
		contentType string
		body        string
	}{
		{"application/jsonx", `{"protocol":"filees.public-share-backchannel/v1","alias":"a","slug":"b"}`},
		{"application/json", strings.Repeat(" ", (2<<20)+1)},
	} {
		request := httptest.NewRequest(http.MethodPost, "http://authority/v1/entry", strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("content-type=%q status=%d", test.contentType, recorder.Code)
		}
	}
}

func TestClientRejectsMalformedAuthorityMetadata(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cache_key":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz","display_name":"x","revision":7}`)
	})
	client := Client{BaseURL: "http://authority", HTTP: &http.Client{Transport: handlerTransport{handler: handler}}}
	_, err := client.Check(context.Background(), authority.ObjectRequest{Revision: 7})
	if err == nil {
		t.Fatal("client accepted non-hex cache key")
	}
}

func TestFetchConcurrencyLimitFailsClosed(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	server := Server{Authority: stubAuthority{body: "payload"}, FetchSlots: slots}
	request := httptest.NewRequest(http.MethodPost, "http://authority/v1/fetch", strings.NewReader(`{"protocol":"filees.public-share-backchannel/v1","object":{"channel_id":"`+uuid.NewString()+`","public_id":"7f3a1c9e2b4d6a80","revision":7,"frost_proof":"proof"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("saturated authority fetch status=%d", recorder.Code)
	}
}
