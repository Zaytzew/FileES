package reservationclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	reservationv1 "filees/pkg/reservation/v1"
)

func TestBatchPinnedSSHNegotiation(t *testing.T) {
	for _, mode := range []string{"ok", "old-schema", "old-field", "denied", "broken", "trailing", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			host, _ := generateKey(t)
			identity, private := generateKey(t)
			address := startReservationSSHResponse(t, host, identity.PublicKey(), func(in *bufio.Reader, out, stderr *bytes.Buffer) uint32 {
				line, _ := in.ReadString('\n')
				req, err := reservationv1.ParseBatchRequest([]byte(line))
				if err != nil {
					t.Error(err)
					return 1
				}
				switch mode {
				case "old-schema":
					stderr.WriteString("reservation request schema mismatch")
					return 1
				case "old-field":
					stderr.WriteString(`json: unknown field "scope"`)
					return 1
				case "denied":
					stderr.WriteString("access denied")
					return 1
				case "broken":
					out.WriteString(`{"schema":"broken"}`)
					return 0
				case "oversize":
					out.Write(bytes.Repeat([]byte("x"), reservationv1.BatchMaxResponseBytes+1))
					return 0
				}
				json.NewEncoder(out).Encode(reservationv1.BatchResponse{Schema: reservationv1.BatchSchema, Scope: req.Scope, ServerID: "lab", ClientID: testRepoID, RealmID: testRepoID, Repositories: []string{}, Entries: []reservationv1.BatchEntry{}})
				if mode == "trailing" {
					out.WriteString("{}")
				}
				return 0
			})
			c := configuredClient(t, address, host.PublicKey(), private)
			_, err := c.FetchBatch(t.Context(), reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: strings.Repeat("a", 64)})
			if (err == nil) != (mode == "ok") || errors.Is(err, ErrBatchUnsupported) != (strings.HasPrefix(mode, "old-")) {
				t.Fatalf("%s: %v", mode, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = c.FetchBatch(ctx, reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: strings.Repeat("a", 64)}); err == nil || errors.Is(err, ErrBatchUnsupported) {
				t.Fatal("cancel negotiated legacy")
			}
		})
	}
}
