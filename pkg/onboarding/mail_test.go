package onboarding

import (
	"bytes"
	"testing"
	"time"
)

func TestRenderMailIsStableAndHeaderSafe(t *testing.T) {
	created := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)
	job := MailJob{
		Entry:     MailOutboxEntry{MessageID: "7b807185-aa75-4169-8a65-705c7cbab176", DeliveryAddress: "User@Example.COM", OTP: "AAAAAAAA-BBBBBBBBBBBBBBBB", DeliveryState: DeliverySending, CreatedAt: created},
		ExpiresAt: created.Add(30 * time.Minute),
	}
	first, err := RenderMail(job, "filees@example.net", "mail.example.net")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderMail(job, "filees@example.net", "mail.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("retry changed message bytes")
	}
	for _, expected := range [][]byte{[]byte("Message-ID: <7b807185-aa75-4169-8a65-705c7cbab176@mail.example.net>\r\n"), []byte("To: <User@example.com>\r\n"), []byte(job.Entry.OTP)} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("message misses %q:\n%s", expected, first)
		}
	}
	if bytes.Contains(first, []byte("\n")) && bytes.Contains(bytes.ReplaceAll(first, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("message contains bare LF")
	}
	job.Entry.MessageID = "bad\r\nBcc: victim@example.net"
	if _, err := RenderMail(job, "filees@example.net", "mail.example.net"); err == nil {
		t.Fatal("header injection accepted")
	}
}
