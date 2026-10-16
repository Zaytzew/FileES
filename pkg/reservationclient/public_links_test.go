package reservationclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"filees/pkg/publicstate"
	"github.com/google/uuid"
)

func TestPublicLinksPinnedSSHAndExplicitNegotiation(t *testing.T) {
	for _, mode := range []string{"success", "old", "denied", "malformed", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			host, _ := generateKey(t)
			client, private := generateKey(t)
			address := startReservationSSHResponse(t, host, client.PublicKey(), func(in *bufio.Reader, out, stderr *bytes.Buffer) uint32 {
				line, _ := in.ReadString('\n')
				req, err := publicstate.ParseRequest([]byte(line))
				if err != nil {
					t.Error(err)
					return 1
				}
				switch mode {
				case "old":
					stderr.WriteString("reservation request schema mismatch")
					return 1
				case "denied":
					stderr.WriteString("access denied")
					return 1
				case "malformed":
					out.WriteString(`{"schema":"broken"}`)
					return 0
				}
				r, _ := publicstate.Build(publicstate.Binding{ServerID: "lab", ClientID: uuid.NewString(), RealmID: uuid.NewString()}, req, nil)
				_ = json.NewEncoder(out).Encode(r)
				if mode == "trailing" {
					out.WriteString("{}")
				}
				return 0
			})
			c := configuredClient(t, address, host.PublicKey(), private)
			_, err := c.FetchPublicLinks(t.Context(), publicstate.Request{Schema: publicstate.Schema})
			if (err == nil) != (mode == "success") || errors.Is(err, ErrPublicLinksUnsupported) != (mode == "old") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}
