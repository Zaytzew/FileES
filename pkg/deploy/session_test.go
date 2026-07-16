package deploy

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTunnelSessionIsStrictAndPinsOneEd25519Key(t *testing.T) {
	key, err := BootstrapAuthorizedKey()
	if err != nil {
		t.Fatal(err)
	}
	wanted := TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: uuid.NewString(), HelperHostPublicKey: key}
	raw, err := EncodeTunnelSession(wanted)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTunnelSession(strings.NewReader(string(raw)))
	if err != nil || got != wanted {
		t.Fatalf("session=%+v err=%v", got, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"schema":"filees.tunnel-session/v1","deploy_request_id":"bad","helper_host_public_key":"` + key + `"}`,
		string(raw) + `{}`,
		`{"schema":"filees.tunnel-session/v1","deploy_request_id":"` + uuid.NewString() + `","helper_host_public_key":"ssh-rsa AAAA"}`,
	} {
		if _, err := DecodeTunnelSession(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted invalid tunnel frame: %q", raw)
		}
	}
}
