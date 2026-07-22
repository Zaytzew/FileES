package releaseenvelope

import (
	"strings"
	"testing"
	"time"
)

const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseSelectAndBindArtifactManifest(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	envelope, err := ParseEnvelope([]byte(`{
  "schema_version":2,"release_id":"r181","sequence":181,"security_epoch":1,
  "key_id":"release-2026-a","expires_at":"2026-07-23T12:00:00Z",
  "components":[
    {"name":"server","platform":"openbsd-amd64","manifest":"releases/r181/server/openbsd-amd64/manifest.json"},
    {"name":"desktop","platform":"linux-amd64","manifest":"releases/r181/desktop/linux-amd64/manifest.json"},
    {"name":"android","platform":"android-arm64","manifest":"releases/r181/android/android-arm64/manifest.json"}
  ]
}`), now)
	if err != nil {
		t.Fatal(err)
	}
	component, err := envelope.Select("desktop", "linux-amd64")
	if err != nil || component.Manifest != "releases/r181/desktop/linux-amd64/manifest.json" {
		t.Fatalf("selected component = %+v, %v", component, err)
	}
	manifest, err := ParseArtifactManifest([]byte(`{
  "schema_version":2,"release_id":"r181","sequence":181,"security_epoch":1,
  "key_id":"release-2026-a","component":"desktop","platform":"linux-amd64","version":"1.1.0",
  "artifacts":[{"source":"filees-client-linux-amd64.tar.gz","sha256":"` + digest + `","size":1234,"kind":"bundle"}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.ValidateManifest(component, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Sequence++
	if err := envelope.ValidateManifest(component, manifest); err == nil {
		t.Fatal("accepted manifest from another sequence")
	}
}

func TestEnvelopeRejectsExpiredDuplicateUnsafeAndUnknown(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	base := `{"schema_version":2,"release_id":"r1","sequence":1,"security_epoch":1,"key_id":"key-1","expires_at":"2026-07-23T00:00:00Z","components":[{"name":"desktop","platform":"linux-amd64","manifest":"releases/r1/desktop/linux-amd64/manifest.json"}]}`
	cases := []string{
		strings.Replace(base, `"2026-07-23T00:00:00Z"`, `"2026-07-22T11:00:00Z"`, 1),
		strings.Replace(base, `]}`, `,{"name":"desktop","platform":"linux-amd64","manifest":"other/manifest.json"}]}`, 1),
		strings.Replace(base, `releases/r1/desktop/linux-amd64/manifest.json`, `../manifest.json`, 1),
		strings.Replace(base, `"components"`, `"unknown":true,"components"`, 1),
		base + `{}`,
	}
	for index, raw := range cases {
		if _, err := ParseEnvelope([]byte(raw), now); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestArtifactManifestRejectsInvalidDigestSizeDuplicateAndIdentity(t *testing.T) {
	base := `{"schema_version":2,"release_id":"r1","sequence":1,"security_epoch":1,"key_id":"key-1","component":"desktop","platform":"linux-amd64","version":"1.0","artifacts":[{"source":"client.tar.gz","sha256":"` + digest + `","size":12,"kind":"bundle"}]}`
	cases := []string{
		strings.Replace(base, digest, "deadbeef", 1),
		strings.Replace(base, `"size":12`, `"size":0`, 1),
		strings.Replace(base, `]}`, `,{"source":"client.tar.gz","sha256":"`+digest+`","size":1,"kind":"bundle"}]}`, 1),
		strings.Replace(base, `client.tar.gz`, `../client.tar.gz`, 1),
		strings.Replace(base, `"sequence":1`, `"sequence":0`, 1),
	}
	for index, raw := range cases {
		if _, err := ParseArtifactManifest([]byte(raw)); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}
