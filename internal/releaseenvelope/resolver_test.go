package releaseenvelope

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type mapFetcher map[string][]byte

func (fetcher mapFetcher) Cat(_ context.Context, path string) ([]byte, error) {
	data, ok := fetcher[path]
	if !ok {
		return nil, errors.New("not found: " + path)
	}
	return data, nil
}

type signatureVerifier struct {
	accepted map[string]string
	calls    []string
}

func (verifier *signatureVerifier) Verify(_ context.Context, keyID string, message, signature []byte) error {
	verifier.calls = append(verifier.calls, keyID+":"+string(signature))
	if verifier.accepted[string(signature)] == keyID {
		return nil
	}
	return errors.New("bad signature")
}

func TestResolverVerifiesEnvelopeBeforeTrustingKeyIDAndBindsManifest(t *testing.T) {
	envelope := []byte(`{"schema_version":2,"release_id":"r181","sequence":181,"security_epoch":1,"key_id":"key-new","expires_at":"2026-07-23T00:00:00Z","components":[{"name":"desktop","platform":"linux-amd64","manifest":"releases/r181/desktop/linux-amd64/manifest.json"}]}`)
	manifest := []byte(`{"schema_version":2,"release_id":"r181","sequence":181,"security_epoch":1,"key_id":"key-new","component":"desktop","platform":"linux-amd64","version":"1.1","artifacts":[{"source":"client.tar.gz","sha256":"` + digest + `","size":12,"kind":"bundle"}]}`)
	verifier := &signatureVerifier{accepted: map[string]string{"envelope-sig": "key-new", "manifest-sig": "key-new"}}
	resolved, err := (Resolver{
		Fetcher: mapFetcher{
			"channels/stable.v2.json": envelope, "channels/stable.v2.json.sig": []byte("envelope-sig"),
			"releases/r181/desktop/linux-amd64/manifest.json":     manifest,
			"releases/r181/desktop/linux-amd64/manifest.json.sig": []byte("manifest-sig"),
		},
		Verifier: verifier, TrustedKeys: []string{"key-old", "key-new"},
		Now: func() time.Time { return time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC) },
	}).Resolve(context.Background(), "channels/stable.v2.json", "desktop", "linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SigningKeyID != "key-new" || resolved.Manifest.Version != "1.1" {
		t.Fatalf("resolved = %+v", resolved)
	}
	wantCalls := []string{"key-old:envelope-sig", "key-new:envelope-sig", "key-new:manifest-sig"}
	if len(verifier.calls) != len(wantCalls) {
		t.Fatalf("verification calls = %v", verifier.calls)
	}
	for index := range wantCalls {
		if verifier.calls[index] != wantCalls[index] {
			t.Fatalf("verification calls = %v", verifier.calls)
		}
	}
}

func TestResolverRejectsEnvelopeKeyConfusionAndManifestReplay(t *testing.T) {
	envelope := []byte(`{"schema_version":2,"release_id":"r1","sequence":2,"security_epoch":1,"key_id":"claimed-key","expires_at":"2026-07-23T00:00:00Z","components":[{"name":"desktop","platform":"linux-amd64","manifest":"r/manifest.json"}]}`)
	fetcher := mapFetcher{"channels/stable.v2.json": envelope, "channels/stable.v2.json.sig": []byte("sig")}
	verifier := &signatureVerifier{accepted: map[string]string{"sig": "actual-key"}}
	_, err := (Resolver{Fetcher: fetcher, Verifier: verifier, TrustedKeys: []string{"actual-key"}, Now: func() time.Time { return time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC) }}).Resolve(context.Background(), "channels/stable.v2.json", "desktop", "linux-amd64")
	if err == nil || !strings.Contains(err.Error(), "does not match signing key") {
		t.Fatalf("key confusion error = %v", err)
	}
}
