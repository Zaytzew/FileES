package repoworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

// Exercise the dispatcher and real channel store, not just the JSON parser:
// create, update and list must all retain the complete 4096-file description.
func TestDispatcherLargePublicShareRoundTrip(t *testing.T) {
	owner, repo, client := uuid.NewString(), uuid.NewString(), uuid.NewString()
	channels := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "pracownia"}, TokenKey: []byte(strings.Repeat("t", 32))}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := Dispatcher{Worker: &Worker{Store: store, PublicShares: ChannelPublicShareService{Channels: channels}}, Resolver: guiBlobResolver{client: Session{ClientID: client, RealmID: owner, CanCreateRepositories: true}}}
	declaration := control.PublicShareDeclaration{RepoID: repo, SourceRoot: "wydanie", Slug: "archiwum"}
	for i := 0; i < 4096; i++ {
		name := fmt.Sprintf("projekt-archiwalny/rysunki/Załącznik-%04d.dwg", i)
		declaration.Objects = append(declaration.Objects, control.PublicShareObject{PublicID: fmt.Sprintf("%016x", i), RepoPath: "wydanie/" + name, DisplayName: name})
	}
	exchange := func(kind control.TicketType, payload any, large bool) control.Result {
		t.Helper()
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), kind, client, payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(ticket)
		if err != nil {
			t.Fatal(err)
		}
		if large && len(raw) <= control.DefaultMessageBytes {
			t.Fatal("fixture does not cross old limit")
		}
		var out bytes.Buffer
		if err := d.Serve(t.Context(), client, bytes.NewReader(append(raw, '\n')), &out); err != nil {
			t.Fatal(err)
		}
		result, err := control.ParseResult(bytes.TrimSpace(out.Bytes()))
		if err != nil || result.Status != control.ResultOK {
			t.Fatalf("result=%+v failure=%+v err=%v", result, result.Error, err)
		}
		return result
	}
	created := exchange(control.TicketCreatePublicShare, control.CreatePublicSharePayload{PublicShareDeclaration: declaration}, true)
	var creation control.PublicShareResult
	if err := control.DecodePayload(created.Result, &creation); err != nil {
		t.Fatal(err)
	}
	declaration.Objects[4095].DisplayName = "Zmieniona nazwa.dwg"
	exchange(control.TicketUpdatePublicShare, control.UpdatePublicSharePayload{ChannelID: creation.ChannelID, PublicShareDeclaration: declaration}, true)
	listed := exchange(control.TicketListPublicShares, control.ListPublicSharesPayload{RepoID: repo}, false)
	var list control.ListPublicSharesResult
	if err := control.DecodePayload(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Shares) != 1 || len(list.Shares[0].Objects) != 4096 || list.Shares[0].Objects[4095].DisplayName != "Zmieniona nazwa.dwg" {
		t.Fatal("large declaration was truncated or not updated")
	}
	if len(listed.Result) <= control.DefaultMessageBytes {
		t.Fatal("listing fixture does not cross old response limit")
	}
}

func TestDispatcherSizeBoundaries(t *testing.T) {
	for _, kind := range []control.TicketType{control.TicketClientDeactivate, control.TicketCreatePublicShare, control.TicketUpdatePublicShare} {
		for _, over := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/over=%d", kind, over), func(t *testing.T) {
				// Whitespace counts towards the wire budget. An otherwise invalid
				// envelope at the exact limit must reach validation, not size rejection.
				raw := fmt.Sprintf(`{"type":%q}`, kind)
				raw += strings.Repeat(" ", control.TicketByteLimit(kind)+over-len(raw))
				d := Dispatcher{Worker: &Worker{}, Resolver: guiBlobResolver{}}
				err := d.Serve(t.Context(), "unused", strings.NewReader(raw), io.Discard)
				if err == nil || errors.Is(err, control.ErrTicketTooLarge) != (over == 1) {
					t.Fatalf("boundary result: %v", err)
				}
			})
		}
	}
}
