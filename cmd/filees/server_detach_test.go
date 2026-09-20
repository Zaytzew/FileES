package main

import (
	"context"
	"errors"
	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/detachment"
	"filees/pkg/localrepo"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestServerDetachRetainsIntentAcrossLostReplyAndRestart(t *testing.T) {
	for _, terminalRefusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "refusal"}[terminalRefusal], func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root := clientprofile.DefaultRoot()
			profile := clientprofile.Profile{ServerID: "test", ClientID: uuid.NewString(), DisplayName: "Test"}
			credential := filepath.Join(root, "test", "key")
			if err := os.MkdirAll(filepath.Dir(credential), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credential, []byte("test-only"), 0600); err != nil {
				t.Fatal(err)
			}
			userFile := filepath.Join(t.TempDir(), "keep.txt")
			if err := os.WriteFile(userFile, []byte("user data"), 0600); err != nil {
				t.Fatal(err)
			}
			localPath, detachmentPath := filepath.Join(t.TempDir(), "local.json"), filepath.Join(t.TempDir(), "detached.json")
			local, err := localrepo.Open(localPath)
			if err != nil {
				t.Fatal(err)
			}
			notices, err := detachment.Open(detachmentPath)
			if err != nil {
				t.Fatal(err)
			}
			var first control.Ticket
			service := &serverDetachService{local: local, provisioner: newDaemonProvisioner(local, nil, []clientprofile.Profile{profile}), profileRoot: root, detachments: notices,
				exchange: func(_ context.Context, p clientprofile.Profile, ticket control.Ticket) (control.Result, error) {
					saved, err := readServerDetach(root, p)
					if err != nil || !reflect.DeepEqual(saved.Ticket, ticket) {
						t.Fatalf("ticket was not durable before exchange: %v", err)
					}
					first = ticket
					return control.Result{}, io.ErrUnexpectedEOF
				},
			}
			if err := service.Detach(t.Context(), profile.ServerID); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("lost reply: %v", err)
			}
			if notices.Current(profile.ServerID) {
				t.Fatal("EOF was guessed to mean success")
			}
			if _, err := os.Stat(credential); err != nil {
				t.Fatalf("EOF removed credential: %v", err)
			}
			if got := refusedProfileCause(profile, time.Now()); got != detachment.CauseSelf {
				t.Fatalf("terminal observer lost own pending intent: %s", got)
			}
			other := profile
			other.ClientID = uuid.NewString()
			if got := refusedProfileCause(other, time.Now()); got == detachment.CauseSelf {
				t.Fatal("old intent attributed to another activation")
			}
			local, err = localrepo.Open(localPath)
			if err != nil {
				t.Fatal(err)
			}
			notices, err = detachment.Open(detachmentPath)
			if err != nil {
				t.Fatal(err)
			}
			restarted := &serverDetachService{local: local, provisioner: newDaemonProvisioner(local, nil, []clientprofile.Profile{profile}), profileRoot: root, detachments: notices,
				exchange: func(_ context.Context, p clientprofile.Profile, ticket control.Ticket) (control.Result, error) {
					if !reflect.DeepEqual(first, ticket) {
						t.Fatal("restart issued a new operation")
					}
					if terminalRefusal {
						return control.Result{}, controlclient.ErrIdentityRefused
					}
					return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ClientDeactivateResult{ServiceRevision: 19}, time.Now())
				},
			}
			if err := restarted.Detach(t.Context(), profile.ServerID); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(credential); !os.IsNotExist(err) {
				t.Fatalf("credential survived: %v", err)
			}
			loaded, err := detachment.Open(detachmentPath)
			if err != nil {
				t.Fatal(err)
			}
			if !loaded.Current(profile.ServerID) || loaded.List()[0].Cause != detachment.CauseSelf {
				t.Fatal("no durable self-detachment fence")
			}
			if _, ok := restarted.provisioner.Profile(profile.ServerID); ok {
				t.Fatal("profile still active")
			}
			if err := restarted.Detach(t.Context(), profile.ServerID); err != nil {
				t.Fatalf("completed detach not idempotent: %v", err)
			}
			if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "user data" {
				t.Fatalf("user file changed: %q %v", raw, err)
			}
		})
	}
}

func TestServerDetachRejectsCorruptOrForeignIntent(t *testing.T) {
	root := t.TempDir()
	p := clientprofile.Profile{ServerID: "server", ClientID: uuid.NewString()}
	intent, err := prepareServerDetach(root, p, []string{"/preserved/project"})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := prepareServerDetach(root, p, nil)
	if err != nil || !reflect.DeepEqual(resumed, intent) {
		t.Fatalf("intent changed: %+v %v", resumed, err)
	}
	p.ClientID = uuid.NewString()
	if _, err := prepareServerDetach(root, p, nil); err == nil {
		t.Fatal("foreign activation reused intent")
	}
	path, err := serverDetachPath(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareServerDetach(root, p, nil); err == nil {
		t.Fatal("corrupt intent replaced with fresh operation")
	}
}
