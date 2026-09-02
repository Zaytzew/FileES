package v1

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

const repoID = "f5d5bfee-62f4-5b9c-b26f-8d4c424fb8f0"

func TestRequestValidateRequiresSchemaAndUUID(t *testing.T) {
	if err := (Request{Schema: Schema, RepoID: repoID}).Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if (Request{Schema: "wrong", RepoID: repoID}).Validate() == nil {
		t.Fatal("wrong schema accepted")
	}
	if (Request{Schema: Schema, RepoID: "not-a-uuid"}).Validate() == nil {
		t.Fatal("non-UUID repo id accepted")
	}
}

func TestParseRequestRoundTrips(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `"}`)
	req, err := ParseRequest(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.RepoID != repoID {
		t.Fatalf("req=%+v", req)
	}
}

func TestParseRequestRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","extra":"nope"}`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func TestParseResultRoundTrips(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","reservations":[{"path":"a.txt","token":"tok"}],"generation":"3","as_of":"2026-09-01T12:00:00Z"}`)
	res, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Reservations) != 1 || res.Generation != "3" {
		t.Fatalf("res=%+v", res)
	}
}

func TestParseResultRejectsFreshWithoutAsOf(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","generation":"3"}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected fresh result missing as_of to be rejected")
	}
}

func TestParseResultRejectsFreshWithoutGeneration(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","as_of":"2026-09-01T12:00:00Z"}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected fresh result missing generation to be rejected")
	}
}

func TestParseResultRejectsUnknownFakingAsOf(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","unknown":true,"as_of":"2026-09-01T12:00:00Z"}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected unknown result carrying as_of to be rejected")
	}
}

func TestParseResultRejectsTrailingDataAfterJSON(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","as_of":"2026-09-01T12:00:00Z","generation":"1"}{"schema":"evil"}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected a second smuggled JSON value to be rejected")
	}
}

func TestParseRequestRejectsTrailingDataAfterJSON(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `"} garbage`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("expected trailing garbage after the JSON value to be rejected")
	}
}

func TestParseResultRejectsUnknownCombiningStaleAndUnknown(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","unknown":true,"stale":true}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected unknown+stale combination to be rejected")
	}
}

func TestParseResultRejectsUnknownCarryingReservations(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `","repo_id":"` + repoID + `","unknown":true,"reservations":[{"path":"a.txt","token":"tok"}]}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected unknown carrying reservations to be rejected")
	}
}

func TestParseResultRequiresRepoID(t *testing.T) {
	raw := []byte(`{"schema":"` + Schema + `"}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected missing repo id to be rejected")
	}
}

// The client cannot tell a quiet server from one that has stopped producing
// its view: both give a successful fetch and an unchanged generation, and only
// the age grows. So the answer has to come from the server, and it travels
// here rather than on a lane of its own.
func TestResultCarriesWhenTheServerLastProducedTheView(t *testing.T) {
	produced := time.Date(2026, 8, 23, 17, 38, 6, 0, time.UTC)
	result := Result{
		Schema: Schema, RepoID: "8e2d00e8-0190-5471-a2af-814399471f13",
		ViewGeneration: 14, ViewGeneratedAt: &produced,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ViewGeneration != 14 {
		t.Fatalf("view generation must survive the wire, got %d", decoded.ViewGeneration)
	}
	if decoded.ViewGeneratedAt == nil || !decoded.ViewGeneratedAt.Equal(produced) {
		t.Fatalf("view production time must survive the wire, got %s", decoded.ViewGeneratedAt)
	}
}

// A server that has not published anything for this client must not be
// indistinguishable from one that simply omitted the field, so absence is
// encoded as absence rather than as a zero time.
func TestUnpublishedViewIsOmittedRatherThanZeroed(t *testing.T) {
	raw, err := json.Marshal(Result{Schema: Schema, RepoID: "8e2d00e8-0190-5471-a2af-814399471f13"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("view_generated_at")) {
		t.Fatalf("an unset production time must not appear on the wire: %s", raw)
	}
}
