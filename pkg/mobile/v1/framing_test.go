package v1

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFrameRoundTripWithPayload(t *testing.T) {
	req, err := NewRequest(uuid.NewString(), OpUploadObject, UploadObjectPayload{
		RepoID: "r", ParentPath: "photos", Filename: "a.jpg", Size: 4, Sha256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(req)
	payload := []byte("\x00\x01\x02\x03")

	var buf bytes.Buffer
	if err := WriteFrame(&buf, RequestMagic, header, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	gotHeader, gotPayload, err := ReadFrame(&buf, RequestMagic, MaxHeaderBytes)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(gotHeader, header) {
		t.Fatalf("header mismatch")
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload mismatch: %v", gotPayload)
	}
	if _, err := ParseRequest(gotHeader); err != nil {
		t.Fatalf("decoded header invalid: %v", err)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, ResponseMagic, []byte(`{"k":1}`), nil); err != nil {
		t.Fatal(err)
	}
	_, payload, err := ReadFrame(&buf, ResponseMagic, MaxHeaderBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(payload))
	}
}

func TestFrameRejectsWrongMagic(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, RequestMagic, []byte(`{}`), nil)
	if _, _, err := ReadFrame(&buf, ResponseMagic, MaxHeaderBytes); err == nil {
		t.Fatal("expected magic mismatch rejection")
	}
}

func TestFrameRejectsOversizedHeader(t *testing.T) {
	// Header declares a length above the cap.
	frame := RequestMagic + "\n" + "1000000\n" + strings.Repeat("x", 10)
	if _, _, err := ReadFrame(strings.NewReader(frame), RequestMagic, MaxHeaderBytes); err == nil {
		t.Fatal("expected oversized-header rejection")
	}
}

func TestFrameRejectsBadLength(t *testing.T) {
	frame := RequestMagic + "\n" + "notnum\n" + "{}"
	if _, _, err := ReadFrame(strings.NewReader(frame), RequestMagic, MaxHeaderBytes); err == nil {
		t.Fatal("expected invalid-length rejection")
	}
}

func TestFrameRejectsUnterminatedMagicLine(t *testing.T) {
	// No newline within the line limit.
	if _, _, err := ReadFrame(strings.NewReader(strings.Repeat("A", 200)), RequestMagic, MaxHeaderBytes); err == nil {
		t.Fatal("expected over-long line rejection")
	}
}
