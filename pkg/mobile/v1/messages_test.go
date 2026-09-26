package v1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func rid() string { return uuid.NewString() }

func mustRequest(t *testing.T, op Operation, payload any) Request {
	t.Helper()
	req, err := NewRequest(rid(), op, payload)
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", op, err)
	}
	return req
}

func TestRequestDesktopJoinRejectsMissingEmailAndInviteBlob(t *testing.T) {
	if _, err := NewRequest(rid(), OpRequestDesktopJoin, RequestDesktopJoinPayload{}); err == nil {
		t.Fatal("empty email must fail")
	}
	for _, email := range []string{"not-an-email", "a@b@c.test", " a@b.test", "a@", "@b.test", strings.Repeat("a", 253) + "@b"} {
		if _, err := NewRequest(rid(), OpRequestDesktopJoin, RequestDesktopJoinPayload{Email: email}); err == nil {
			t.Fatalf("email %q must fail", email)
		}
	}
	ok, err := NewSuccess(rid(), OpRequestDesktopJoin, RequestDesktopJoinResult{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(t, ok.Result)), "invite") || strings.Contains(string(mustJSON(t, ok.Result)), "token") {
		t.Fatalf("join result must not carry an invite: %s", ok.Result)
	}
}

func TestRequestDesktopJoinRejectsInviteFieldsOnTheWire(t *testing.T) {
	id := rid()
	req := []byte(`{"schema":"filees.mobile/v1","request_id":"` + id + `","operation":"REQUEST_DESKTOP_JOIN","payload":{"email":"desk@example.test","invite":"nope"}}`)
	if _, err := ParseRequest(req); err == nil {
		t.Fatal("unknown invite field on the request must fail")
	}
	resp := []byte(`{"schema":"filees.mobile/v1","request_id":"` + id + `","operation":"REQUEST_DESKTOP_JOIN","status":"ok","result":{"invite":"secret"}}`)
	if _, err := ParseResponse(resp); err == nil {
		t.Fatal("invite field on the result must fail")
	}
}

func mustJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func TestRequestRoundTripAllOperations(t *testing.T) {
	cases := []struct {
		op      Operation
		payload any
	}{
		{OpRefreshManifest, RefreshManifestPayload{RepoID: "repo-1", KnownViewGeneration: 87, KnownRepoRevision: 1847}},
		{OpListRepositories, ListRepositoriesPayload{}},
		{OpListDirectory, ListDirectoryPayload{RepoID: "repo-1", Path: "02_Fotografie/2026-07-20"}},
		{OpReadObject, ReadObjectPayload{RepoID: "repo-1", Path: "02_Fotografie/IMG_0012.jpg"}},
		{OpUploadObject, UploadObjectPayload{RepoID: "repo-1", ParentPath: "photos", Filename: "IMG_0013.jpg", Size: 5123401, Sha256: strings.Repeat("a", 64), ContentType: "image/jpeg"}},
		{OpUploadTree, UploadTreePayload{RepoID: "repo-1", ParentPath: "mobile-uploads", FileCount: 12, Size: 4096, Sha256: strings.Repeat("a", 64)}},
		{OpOperationStatus, OperationStatusPayload{TargetRequestID: rid()}},
		{OpRequestDesktopJoin, RequestDesktopJoinPayload{Email: "desk@example.test"}},
	}
	for _, c := range cases {
		req := mustRequest(t, c.op, c.payload)
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal %s: %v", c.op, err)
		}
		got, err := ParseRequest(raw)
		if err != nil {
			t.Fatalf("ParseRequest %s: %v", c.op, err)
		}
		if got.Operation != c.op || got.RequestID != req.RequestID {
			t.Fatalf("%s: round-trip mismatch: %+v", c.op, got)
		}
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"schema":"filees.mobile/v1","request_id":"` + rid() + `","operation":"READ_OBJECT","payload":{"repo_id":"r","path":"a","bogus":1}}`)
	if _, err := ParseRequest(raw); err == nil {
		t.Fatal("expected unknown-field rejection in payload")
	}
}

func TestRequestRejectsBadUUID(t *testing.T) {
	_, err := NewRequest("not-a-uuid", OpReadObject, ReadObjectPayload{RepoID: "r", Path: "a"})
	if err == nil {
		t.Fatal("expected UUID rejection")
	}
}

func TestOperationStatusRejectsInvalidTargetRequestID(t *testing.T) {
	for _, target := range []string{"", "not-a-uuid", "../outside", "../../clients/view", "/tmp/outside", `..\outside`} {
		t.Run(target, func(t *testing.T) {
			id := rid()
			payload := OperationStatusPayload{TargetRequestID: target}
			if _, err := NewRequest(id, OpOperationStatus, payload); err == nil {
				t.Error("NewRequest accepted an invalid target_request_id")
			}
			// A hostile peer can bypass NewRequest and send its own JSON.
			rawPayload, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(Request{Schema: Schema, RequestID: id, Operation: OpOperationStatus, Payload: rawPayload})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseRequest(raw); err == nil {
				t.Error("ParseRequest accepted an invalid target_request_id")
			}
		})
	}
}

func TestUploadRejectsPathTraversal(t *testing.T) {
	bad := []UploadObjectPayload{
		{RepoID: "r", ParentPath: "../etc", Filename: "x", Size: 1, Sha256: strings.Repeat("a", 64)},
		{RepoID: "r", ParentPath: "ok", Filename: "../x", Size: 1, Sha256: strings.Repeat("a", 64)},
		{RepoID: "r", ParentPath: "ok", Filename: "a/b", Size: 1, Sha256: strings.Repeat("a", 64)},
		{RepoID: "r", ParentPath: "/abs", Filename: "x", Size: 1, Sha256: strings.Repeat("a", 64)},
		{RepoID: "r", ParentPath: "ok", Filename: "x", Size: 1, Sha256: "SHORT"},
	}
	for i, p := range bad {
		if _, err := NewRequest(rid(), OpUploadObject, p); err == nil {
			t.Fatalf("case %d: expected rejection for %+v", i, p)
		}
	}
}

func TestUploadTreeRejectsOutsideMobileUploads(t *testing.T) {
	_, err := NewRequest(rid(), OpUploadTree, UploadTreePayload{
		RepoID: "r", ParentPath: "photos", FileCount: 2, Size: 1, Sha256: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("expected rejection for parent outside mobile-uploads/")
	}
}

func TestUploadAcceptsRootParent(t *testing.T) {
	if _, err := NewRequest(rid(), OpUploadObject, UploadObjectPayload{RepoID: "r", ParentPath: "", Filename: "top.bin", Size: 1, Sha256: strings.Repeat("f", 64)}); err != nil {
		t.Fatalf("empty parent_path (root) should be allowed: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	sha := "sha256:" + strings.Repeat("a", 64)
	ok := Manifest{Schema: ManifestSchema, RepoID: "r", ViewGeneration: 1, RepoRevision: 0, Complete: true, Entries: []ManifestEntry{
		{Path: "a/b.jpg", Kind: KindFile, Size: 10, LastChangedRevision: 3, ContentHash: &sha},
		{Path: "a", Kind: KindDirectory, LastChangedRevision: 3, ContentHash: nil},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	bad := &Manifest{Schema: ManifestSchema, RepoID: "r", ViewGeneration: 0}
	if err := bad.Validate(); err == nil {
		t.Fatal("view_generation < 1 should be rejected")
	}

	dupHash := "sha256:" + strings.Repeat("b", 64)
	dup := Manifest{Schema: ManifestSchema, RepoID: "r", ViewGeneration: 2, Entries: []ManifestEntry{
		{Path: "x", Kind: KindFile, LastChangedRevision: 1, ContentHash: &dupHash},
		{Path: "x", Kind: KindFile, LastChangedRevision: 1, ContentHash: &dupHash},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate path should be rejected")
	}

	garbage := "not-a-hash"
	badHash := Manifest{Schema: ManifestSchema, RepoID: "r", ViewGeneration: 1, Entries: []ManifestEntry{
		{Path: "x", Kind: KindFile, LastChangedRevision: 1, ContentHash: &garbage},
	}}
	if err := badHash.Validate(); err == nil {
		t.Fatal("malformed content_hash should be rejected")
	}
}

func TestListRepositoriesResultValidation(t *testing.T) {
	ok := ListRepositoriesResult{
		ViewGeneration:    2,
		RealmID:           uuid.NewString(),
		RealmAlias:        "acme",
		ServerDisplayName: "Serwer testowy",
		Repositories: []RepositorySummary{
			{RepoID: "repo-1", DisplayName: "JANCZEWICE", Access: "rw", State: "active", Purpose: "upload_shelf"},
		},
	}
	resp, err := NewSuccess(rid(), OpListRepositories, ok)
	if err != nil {
		t.Fatalf("valid list result: %v", err)
	}
	raw, _ := json.Marshal(resp)
	if _, err := ParseResponse(raw); err != nil {
		t.Fatalf("list result round-trip: %v", err)
	}

	empty := ListRepositoriesResult{ViewGeneration: 1, RealmID: uuid.NewString(), Repositories: []RepositorySummary{}}
	if _, err := NewSuccess(rid(), OpListRepositories, empty); err != nil {
		t.Fatalf("empty projection should be valid: %v", err)
	}

	if _, err := NewSuccess(rid(), OpListRepositories, ListRepositoriesResult{ViewGeneration: 1, RealmID: uuid.NewString()}); err == nil {
		t.Fatal("nil repositories should be rejected")
	}

	branded := ok
	branded.LeadingColor = "#2D5A3D"
	if _, err := NewSuccess(rid(), OpListRepositories, branded); err != nil {
		t.Fatalf("realm leading color should be valid: %v", err)
	}
	badColor := ok
	badColor.LeadingColor = "orange"
	if _, err := NewSuccess(rid(), OpListRepositories, badColor); err == nil {
		t.Fatal("non-hex leading_color should be rejected")
	}

	named := ok
	named.ServerDisplayName = " Serwer"
	if _, err := NewSuccess(rid(), OpListRepositories, named); err == nil {
		t.Fatal("padded server_display_name should be rejected")
	}
	badPurpose := ok
	badPurpose.Repositories = []RepositorySummary{
		{RepoID: "repo-1", DisplayName: "JANCZEWICE", Access: "rw", State: "active", Purpose: "nope"},
	}
	if _, err := NewSuccess(rid(), OpListRepositories, badPurpose); err == nil {
		t.Fatal("unknown purpose should be rejected")
	}
}

func TestListDrawersResultValidation(t *testing.T) {
	empty := ListDrawersResult{Drawers: []DrawerSummary{}, Assignments: map[string]string{}}
	if _, err := NewSuccess(rid(), OpListDrawers, empty); err != nil {
		t.Fatalf("no drawers is the ordinary state, not an error: %v", err)
	}

	ok := ListDrawersResult{
		Version:     uuid.NewString(),
		Drawers:     []DrawerSummary{{ID: "d-1", Name: "Archiwum"}},
		Assignments: map[string]string{"repo-1": "d-1"},
	}
	resp, err := NewSuccess(rid(), OpListDrawers, ok)
	if err != nil {
		t.Fatalf("valid drawers result: %v", err)
	}
	raw, _ := json.Marshal(resp)
	if _, err := ParseResponse(raw); err != nil {
		t.Fatalf("drawers result round-trip: %v", err)
	}

	if _, err := NewSuccess(rid(), OpListDrawers, ListDrawersResult{}); err != nil {
		t.Fatalf("nil drawers/assignments is the ordinary no-drawers state: %v", err)
	}

	badVersion := ok
	badVersion.Version = "not-a-uuid"
	if _, err := NewSuccess(rid(), OpListDrawers, badVersion); err == nil {
		t.Fatal("non-uuid version should be rejected")
	}

	dupID := ok
	dupID.Drawers = []DrawerSummary{{ID: "d-1", Name: "A"}, {ID: "d-1", Name: "B"}}
	if _, err := NewSuccess(rid(), OpListDrawers, dupID); err == nil {
		t.Fatal("duplicate drawer id should be rejected")
	}

	badID := ok
	badID.Drawers = []DrawerSummary{{ID: "has a space", Name: "A"}}
	if _, err := NewSuccess(rid(), OpListDrawers, badID); err == nil {
		t.Fatal("drawer id outside [a-zA-Z0-9-] should be rejected")
	}

	blankName := ok
	blankName.Drawers = []DrawerSummary{{ID: "d-1", Name: "  "}}
	if _, err := NewSuccess(rid(), OpListDrawers, blankName); err == nil {
		t.Fatal("blank drawer name should be rejected")
	}

	dangling := ListDrawersResult{Drawers: []DrawerSummary{}, Assignments: map[string]string{"repo-1": "d-missing"}}
	if _, err := NewSuccess(rid(), OpListDrawers, dangling); err == nil {
		t.Fatal("assignment naming an unknown drawer should be rejected")
	}
}

func TestRefreshResultNotModified(t *testing.T) {
	resp, err := NewSuccess(rid(), OpRefreshManifest, RefreshManifestResult{NotModified: true})
	if err != nil {
		t.Fatalf("NewSuccess: %v", err)
	}
	raw, _ := json.Marshal(resp)
	if _, err := ParseResponse(raw); err != nil {
		t.Fatalf("NOT_MODIFIED result should be valid: %v", err)
	}

	bad, _ := NewSuccess(rid(), OpRefreshManifest, RefreshManifestResult{NotModified: true, Manifest: &Manifest{Schema: ManifestSchema, RepoID: "r", ViewGeneration: 1}})
	raw2, _ := json.Marshal(bad)
	if _, err := ParseResponse(raw2); err == nil {
		t.Fatal("not_modified with a manifest should be rejected")
	}
}

func TestUploadOutcomeValidation(t *testing.T) {
	committed, err := NewSuccess(rid(), OpUploadObject, UploadObjectResult{Outcome: OutcomeCommitted, Revision: 1848, FinalPath: "photos/IMG_0013.jpg"})
	if err != nil {
		t.Fatalf("committed result: %v", err)
	}
	raw, _ := json.Marshal(committed)
	if _, err := ParseResponse(raw); err != nil {
		t.Fatalf("committed round-trip: %v", err)
	}

	// COMMITTED without a revision must fail.
	if _, err := NewSuccess(rid(), OpUploadObject, UploadObjectResult{Outcome: OutcomeCommitted, FinalPath: "x"}); err == nil {
		t.Fatal("committed without revision should be rejected")
	}

	diff, err := NewSuccess(rid(), OpUploadObject, UploadObjectResult{Outcome: OutcomeNameTakenDiff, ExistingSha256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatalf("name-taken-diff result: %v", err)
	}
	rawDiff, _ := json.Marshal(diff)
	if _, err := ParseResponse(rawDiff); err != nil {
		t.Fatalf("name-taken-diff round-trip: %v", err)
	}
}

func TestErrorResponseShape(t *testing.T) {
	resp, err := NewError(rid(), OpUploadObject, ErrorBody{Code: "proto.invalid_payload", Message: "bad"})
	if err != nil {
		t.Fatalf("NewError: %v", err)
	}
	raw, _ := json.Marshal(resp)
	if _, err := ParseResponse(raw); err != nil {
		t.Fatalf("error response should be valid: %v", err)
	}
	// An error carrying a success result must be rejected.
	tampered := `{"schema":"filees.mobile/v1","request_id":"` + rid() + `","operation":"READ_OBJECT","status":"error","result":{"x":1},"error":{"code":"c","message":"m"}}`
	if _, err := ParseResponse([]byte(tampered)); err == nil {
		t.Fatal("error response with a result payload should be rejected")
	}
}
