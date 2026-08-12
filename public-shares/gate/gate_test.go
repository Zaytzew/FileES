package gate

import (
	"bytes"
	"testing"

	"filees/public-shares/channel"
	"github.com/google/uuid"
)

func openProjection() channel.Projection {
	return channel.Projection{Schema: channel.ProjectionSchema, ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "przetarg-2026", State: channel.StateActive, Objects: []channel.PublicObject{{PublicID: "7f3a1c9e2b4d6a80", DisplayName: "Projekt.pdf"}}}
}

func TestOpenChannelPassword(t *testing.T) {
	p := openProjection()
	verifier, err := HashPassword("poprawne haslo", bytes.NewReader(bytes.Repeat([]byte{0x42}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	p.PasswordHash = verifier
	if _, err := Authorize(p, "", "zle haslo"); err == nil {
		t.Fatal("wrong password accepted")
	}
	principal, err := Authorize(p, "", "poprawne haslo")
	if err != nil || principal.Recipient != Anonymous {
		t.Fatalf("valid password = %+v, %v", principal, err)
	}
}

func TestClosedChannelCannotUseInvitationAsBearer(t *testing.T) {
	p := openProjection()
	p.PasswordHash = ""
	p.Recipients = []channel.PublicRecipient{{InvitationHash: TokenHash("invitation")}}
	if _, err := Authorize(p, "wrong", ""); err == nil {
		t.Fatal("wrong invitation accepted")
	}
	if _, err := Authorize(p, "invitation", "ignored"); err == nil {
		t.Fatal("invitation became a bearer credential")
	}
}

func TestVerifierParametersAreBounded(t *testing.T) {
	for _, verifier := range []string{
		"not-phc",
		"$argon2id$v=19$m=9999999,t=3,p=1$QUFBQUFBQUFBQUFBQUFBQQ$QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE",
		"$argon2id$v=19$m=65536,t=999,p=1$QUFBQUFBQUFBQUFBQUFBQQ$QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE",
	} {
		if _, err := VerifyPassword(verifier, "password"); err == nil {
			t.Fatalf("unbounded/malformed verifier accepted: %s", verifier)
		}
	}
}
