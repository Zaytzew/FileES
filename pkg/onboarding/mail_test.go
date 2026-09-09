package onboarding

import (
	"bytes"
	"testing"
	"time"

	"filees/pkg/realmbranding"
)

func TestRenderMailIsStableAndHeaderSafe(t *testing.T) {
	created := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)
	job := MailJob{
		Entry:     MailOutboxEntry{MessageID: "7b807185-aa75-4169-8a65-705c7cbab176", DeliveryAddress: "User@Example.COM", OTP: "AAAAAAAA-BBBBBBBBBBBBBBBB", DeliveryState: DeliverySending, CreatedAt: created},
		ExpiresAt: created.Add(30 * time.Minute),
	}
	branding := realmbranding.Branding{}
	first, err := RenderMail(job, "filees@example.net", "mail.example.net", branding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderMail(job, "filees@example.net", "mail.example.net", branding)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("retry changed message bytes")
	}
	for _, expected := range [][]byte{
		[]byte("Message-ID: <7b807185-aa75-4169-8a65-705c7cbab176@mail.example.net>\r\n"),
		[]byte("To: <User@example.com>\r\n"),
		[]byte("Content-Type: multipart/alternative;"),
		[]byte("Content-Type: text/html; charset=utf-8"),
		[]byte(job.Entry.OTP),
	} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("message misses %q:\n%s", expected, first)
		}
	}
	if bytes.Contains(first, []byte("multipart/related")) {
		t.Fatal("unbranded mail should not carry a logo part")
	}
	if bytes.Contains(first, []byte("\n")) && bytes.Contains(bytes.ReplaceAll(first, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("message contains bare LF")
	}
	job.Entry.MessageID = "bad\r\nBcc: victim@example.net"
	if _, err := RenderMail(job, "filees@example.net", "mail.example.net", branding); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestRenderMailWithBrandingEmbedsLogo(t *testing.T) {
	created := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)
	job := MailJob{
		Entry:     MailOutboxEntry{MessageID: "7b807185-aa75-4169-8a65-705c7cbab176", DeliveryAddress: "user@example.com", Invitation: "filees-invite:v1:AAAA", Template: InvitationMailTemplate, DeliveryState: DeliverySending, CreatedAt: created},
		ExpiresAt: created.Add(30 * time.Minute),
	}
	message, err := RenderMail(job, "filees@example.net", "mail.example.net", DefaultBranding())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte("Content-Type: multipart/related;"),
		[]byte("Content-ID: <" + logoContentID + ">"),
		[]byte("Content-Transfer-Encoding: base64"),
		[]byte("cid:" + logoContentID),
		[]byte(job.Entry.Invitation),
	} {
		if !bytes.Contains(message, expected) {
			t.Fatalf("branded message misses %q:\n%s", expected, message)
		}
	}
	if bytes.Contains(message, []byte("\n")) && bytes.Contains(bytes.ReplaceAll(message, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("message contains bare LF")
	}
}
