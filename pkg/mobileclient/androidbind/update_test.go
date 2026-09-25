package androidbind

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filees/internal/releaseenvelope"
)

func TestInspectMobileUpdateAbsentWithoutAndroidComponent(t *testing.T) {
	key := newUpdateSigner(t)
	srv := serveUpdate(key)
	defer srv.Close()
	key.publishDesktopOnly()
	offer, err := inspectMobileUpdate(context.Background(), srv.URL, "0.1.16+r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if offer.State != "absent" || offer.URL != "" {
		t.Fatalf("offer = %+v", offer)
	}
}

func TestInspectMobileUpdateOffersTheSignedAPK(t *testing.T) {
	key := newUpdateSigner(t)
	apk := []byte("apk-bytes")
	srv := serveUpdate(key)
	defer srv.Close()
	key.publishAndroid(apk)
	offer, err := inspectMobileUpdate(context.Background(), srv.URL, "0.1.16+r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if offer.State != "available" || offer.Version != "0.1.16.r9" || !strings.HasSuffix(offer.URL, "filees-mobile-0.1.16.r9.apk") {
		t.Fatalf("offer = %+v", offer)
	}
	sum := sha256.Sum256(apk)
	if offer.SHA256 != hex.EncodeToString(sum[:]) || offer.Size != int64(len(apk)) {
		t.Fatalf("offer = %+v", offer)
	}
	again, err := inspectMobileUpdate(context.Background(), srv.URL, "ignored", offer.Sequence)
	if err != nil || again.State != "current" || again.URL != "" {
		t.Fatalf("again = %+v err=%v", again, err)
	}
}

func serveUpdate(key *updateSigner) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := key.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
}

type updateSigner struct {
	number []byte
	files  map[string][]byte
	sign   func([]byte) []byte
}

func newUpdateSigner(t *testing.T) *updateSigner {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	number := make([]byte, 8)
	if _, err := rand.Read(number); err != nil {
		t.Fatal(err)
	}
	payload := append(append([]byte("Ed"), number...), public...)
	encoded, err := releaseenvelope.CanonicalSignifyPublicKey([]byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(payload) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	prevID, prevKey := updateKeyID, releasePublicKey
	updateKeyID = "release-test"
	releasePublicKey = string(encoded)
	t.Cleanup(func() {
		updateKeyID = prevID
		releasePublicKey = prevKey
	})
	return &updateSigner{
		number: number,
		files:  map[string][]byte{},
		sign: func(message []byte) []byte {
			raw := append(append([]byte("Ed"), number...), ed25519.Sign(private, message)...)
			return []byte("untrusted comment: test signature\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
		},
	}
}

func (s *updateSigner) publishDesktopOnly() {
	s.putRelease(nil)
}

func (s *updateSigner) publishAndroid(apk []byte) {
	s.putRelease(apk)
}

func (s *updateSigner) putRelease(apk []byte) {
	components := []map[string]string{{
		"name": "desktop", "platform": "linux-amd64",
		"manifest": "releases/r9/desktop/linux-amd64/manifest.json",
	}}
	if apk != nil {
		sum := sha256.Sum256(apk)
		base := "releases/r9/mobile/android/"
		name := "filees-mobile-0.1.16.r9.apk"
		manifest, _ := json.Marshal(map[string]any{
			"schema_version": 2, "release_id": "r9", "sequence": 9, "security_epoch": 1,
			"key_id": "release-test", "component": "mobile", "platform": "android", "version": "0.1.16.r9",
			"artifacts": []map[string]any{{
				"source": name, "sha256": hex.EncodeToString(sum[:]), "size": len(apk), "kind": "installer",
			}},
		})
		s.files[base+"manifest.json"] = manifest
		s.files[base+"manifest.json.sig"] = s.sign(manifest)
		s.files[base+name] = apk
		components = append(components, map[string]string{
			"name": "mobile", "platform": "android", "manifest": base + "manifest.json",
		})
	}
	envelope, _ := json.Marshal(map[string]any{
		"schema_version": 2, "release_id": "r9", "sequence": 9, "security_epoch": 1,
		"key_id": "release-test", "expires_at": "2099-01-01T00:00:00Z", "components": components,
	})
	s.files[updateChannel] = envelope
	s.files[updateChannel+".sig"] = s.sign(envelope)
	desk := []byte(`{"schema_version":2,"release_id":"r9","sequence":9,"security_epoch":1,"key_id":"release-test","component":"desktop","platform":"linux-amd64","version":"0.1.16+r9","artifacts":[{"source":"filees-client.tar.gz","sha256":"` + strings.Repeat("a", 64) + `","size":10,"kind":"bundle"}]}`)
	s.files["releases/r9/desktop/linux-amd64/manifest.json"] = desk
	s.files["releases/r9/desktop/linux-amd64/manifest.json.sig"] = s.sign(desk)
}
