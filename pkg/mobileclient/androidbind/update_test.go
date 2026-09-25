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

func TestInspectMobileUpdateAbsentWhenTheAndroidChannelIsMissing(t *testing.T) {
	key := newUpdateSigner(t)
	srv := serveUpdate(key)
	defer srv.Close()
	// A desktop envelope on another path must not count as an Android release.
	key.files["channels/beta.v2.json"] = []byte(`{"schema_version":2}`)
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
	same, err := inspectMobileUpdate(context.Background(), srv.URL, "0.1.16.r9", 0)
	if err != nil || same.State != "current" || same.URL != "" || same.Size != 0 {
		t.Fatalf("version match should not offer the apk: %+v err=%v", same, err)
	}
}

func TestChannelReleaseNewerThanInstall(t *testing.T) {
	if channelReleaseIsNewer("0.1.17+r1585", "0.1.17.1585") {
		t.Fatal("the installed revision must not download itself")
	}
	if channelReleaseIsNewer("0.1.17+r1590", "0.1.17.1585") {
		t.Fatal("an older channel must not be downloaded over a newer install")
	}
	if !channelReleaseIsNewer("0.1.17+r1585", "0.1.17.1590") {
		t.Fatal("a later channel revision is an update")
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

func (s *updateSigner) publishAndroid(apk []byte) {
	sum := sha256.Sum256(apk)
	base := "releases/r9/android/"
	name := "filees-mobile-0.1.16.r9.apk"
	manifest, _ := json.Marshal(map[string]any{
		"schema_version": 1, "release_id": "r9", "platform": "android",
		"sequence": 9, "security_epoch": 1, "version": "0.1.16.r9",
		"apk": map[string]any{
			"source": name, "sha256": hex.EncodeToString(sum[:]), "size": len(apk),
		},
	})
	s.files[base+"manifest.json"] = manifest
	s.files[base+"manifest.json.sig"] = s.sign(manifest)
	s.files[base+name] = apk
	channel, _ := json.Marshal(map[string]any{
		"schema_version": 1, "release_id": "r9",
		"manifest": base + "manifest.json", "sequence": 9, "security_epoch": 1,
	})
	s.files[updateChannel] = channel
	s.files[updateChannel+".sig"] = s.sign(channel)
}
