package servertool

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
	"filees/public-shares/manifest"
)

const testVerifier = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func gatedRecord(gate func(*manifest.Share)) channel.Record {
	share := manifest.Share{
		Slug:    "przetarg-2026",
		Objects: []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", DisplayName: "Projekt.pdf"}},
	}
	gate(&share)
	return channel.Record{
		ChannelID: "11111111-2222-3333-4444-555555555555",
		Alias:     "atmprojekt", Slug: "przetarg-2026", State: channel.StateActive,
		Manifest:  &share,
		UpdatedAt: time.Date(2026, 9, 3, 14, 30, 0, 0, time.UTC),
	}
}

// The operator is owed the gate, never the secret. They hold the disk and could
// read the record file, so this is not a security boundary - it keeps the
// credential out of terminal scrollback, shell history, and the bug reports
// people paste into chats.
func TestTheListingNamesTheGateWithoutTheSecret(t *testing.T) {
	var out bytes.Buffer
	writeShareTable(&out, []shareSummary{
		summariseShare(gatedRecord(func(s *manifest.Share) { s.Password = testVerifier })),
	})
	report := out.String()
	if strings.Contains(report, "argon2id") || strings.Contains(report, "AAAA") {
		t.Fatalf("the password verifier reached the operator's terminal: %s", report)
	}
	if !strings.Contains(report, "hasło") {
		t.Fatalf("the operator cannot see that this link is gated at all: %s", report)
	}
}

func TestRecipientTokensNeverAppearAndTheCountDoes(t *testing.T) {
	var out bytes.Buffer
	writeShareTable(&out, []shareSummary{
		summariseShare(gatedRecord(func(s *manifest.Share) { s.Recipients = []string{"a@example.com", "b@example.com"} })),
	})
	report := out.String()
	if strings.Contains(report, "example.com") {
		t.Fatalf("recipient addresses are not the operator's to read: %s", report)
	}
	if !strings.Contains(report, "mail+OTP (2)") {
		t.Fatalf("the operator needs to know how the link is gated and how widely: %s", report)
	}
}

// A pinned release and a link that follows HEAD are different products with
// different promises, and telling them apart is most of what a listing is for.
func TestPinnedAndFollowingAreDistinguished(t *testing.T) {
	pinned := int64(438)
	frozen := summariseShare(gatedRecord(func(s *manifest.Share) { s.DoNotFollow = &pinned }))
	if frozen.Revision != "przypięte r438" {
		t.Fatalf("revision = %q", frozen.Revision)
	}
	if following := summariseShare(gatedRecord(func(*manifest.Share) {})); following.Revision != "śledzi HEAD" {
		t.Fatalf("revision = %q", following.Revision)
	}
}

// A withdrawn record has had its manifest cleared, so its gate and contents are
// genuinely unknown. Printing zero files would read as "published nothing",
// which is a different fact and a wrong one.
func TestAWithdrawnShareSaysUnknownRatherThanZero(t *testing.T) {
	var out bytes.Buffer
	writeShareTable(&out, []shareSummary{summariseShare(channel.Record{
		ChannelID: "11111111-2222-3333-4444-555555555555",
		Alias:     "atmprojekt", Slug: "stare", State: channel.StateRevoked,
		UpdatedAt: time.Date(2026, 9, 3, 14, 30, 0, 0, time.UTC),
	})})
	report := out.String()
	if strings.Contains(report, "    0  ") {
		t.Fatalf("an unknown object count must not be printed as none: %s", report)
	}
	if !strings.Contains(report, "revoked") {
		t.Fatalf("a withdrawn address is exactly what a 404 poses a question about: %s", report)
	}
}

// The empty case is the one an operator meets on a fresh server, and a bare
// header with nothing under it reads as a broken command.
func TestNothingPublishedSaysSoInWords(t *testing.T) {
	var out bytes.Buffer
	writeShareTable(&out, nil)
	if !strings.Contains(out.String(), "nie publikuje żadnych udostępnień") {
		t.Fatalf("report = %q", out.String())
	}
}
