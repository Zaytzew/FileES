package repoworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRepairFixture(t *testing.T, record map[string]any) (serviceWC, repoID, path string) {
	t.Helper()
	serviceWC = t.TempDir()
	repoID = "a53c17e1-5f6a-5591-bd0b-17820c4344b2"
	dir := filepath.Join(serviceWC, "admin", "repositories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, repoID+".json")
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return serviceWC, repoID, path
}

func readRepairedRecord(t *testing.T, path string) repositoryRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

// A record from an older build is brought up to the current shape from
// authoritative sources only, so activation cannot emit an entry that
// clientview.View.Validate would reject - which would blank every repository
// for every client receiving the view, not just this one.
func TestCompleteCanonicalRecordFillsDerivableFields(t *testing.T) {
	serviceWC, repoID, path := writeRepairFixture(t, map[string]any{
		"repo_id":        "a53c17e1-5f6a-5591-bd0b-17820c4344b2",
		"owner_realm_id": "5b2b2595-312c-4e8f-9407-148e2a174033",
		"display_name":   "ARCHIWUM",
		"state":          "initializing",
	})
	publisher := ServicePublisher{ServiceWC: serviceWC}

	gotPath, healed, err := publisher.CompleteCanonicalRecord(repoID, "svn+ssh://_filees-data@spot.example.net/")
	if err != nil {
		t.Fatalf("complete record: %v", err)
	}
	if gotPath != path {
		t.Fatalf("record path = %q, want %q", gotPath, path)
	}
	if strings.Join(healed, ",") != "schema,url,created_at" {
		t.Fatalf("healed = %v, want schema, url and created_at", healed)
	}

	record := readRepairedRecord(t, path)
	if record.Schema != RepositorySchema {
		t.Fatalf("schema = %q", record.Schema)
	}
	if record.URL != "svn+ssh://_filees-data@spot.example.net/"+repoID {
		t.Fatalf("url = %q", record.URL)
	}
	if record.CreatedAt.IsZero() || record.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("created_at = %v, want the record's own mtime", record.CreatedAt)
	}
	// The repair must not touch what it did not fill.
	if record.DisplayName != "ARCHIWUM" || record.State != "initializing" {
		t.Fatalf("repair altered untouched fields: %+v", record)
	}
}

// The empty value of these two IS the default, and clientview requires that a
// default is never serialised: a projection carrying editing_policy:"" is what
// an older strict-decoding client refuses. Writing them would be a regression
// dressed as a repair.
func TestCompleteCanonicalRecordNeverSerialisesDefaults(t *testing.T) {
	serviceWC, repoID, path := writeRepairFixture(t, map[string]any{
		"schema":         RepositorySchema,
		"repo_id":        "a53c17e1-5f6a-5591-bd0b-17820c4344b2",
		"owner_realm_id": "5b2b2595-312c-4e8f-9407-148e2a174033",
		"display_name":   "ARCHIWUM",
		"url":            "svn+ssh://_filees-data@spot.example.net/a53c17e1-5f6a-5591-bd0b-17820c4344b2",
		"state":          "initializing",
		"created_at":     "2026-08-14T19:41:19.701822006Z",
	})
	publisher := ServicePublisher{ServiceWC: serviceWC}

	_, healed, err := publisher.CompleteCanonicalRecord(repoID, "svn+ssh://_filees-data@spot.example.net/")
	if err != nil {
		t.Fatalf("complete record: %v", err)
	}
	if len(healed) != 0 {
		t.Fatalf("healed = %v, want no change on an already-complete record", healed)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"editing_policy", "purpose"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("repair serialised the default for %s: %s", field, raw)
		}
	}
}

// display_name is a name a human chose and reads; owner_realm_id decides who
// may act on the repository. Deriving either would turn a repair into either a
// fabrication or a silent grant, so both are refused by name.
func TestCompleteCanonicalRecordRefusesWhatItCannotDerive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		record  map[string]any
		wantErr string
	}{
		{
			name: "no display name",
			record: map[string]any{
				"schema": RepositorySchema, "repo_id": "a53c17e1-5f6a-5591-bd0b-17820c4344b2",
				"owner_realm_id": "5b2b2595-312c-4e8f-9407-148e2a174033", "state": "initializing",
			},
			wantErr: "display_name",
		},
		{
			name: "no owner realm",
			record: map[string]any{
				"schema": RepositorySchema, "repo_id": "a53c17e1-5f6a-5591-bd0b-17820c4344b2",
				"display_name": "ARCHIWUM", "state": "initializing",
			},
			wantErr: "owner_realm_id",
		},
		{
			name: "foreign schema",
			record: map[string]any{
				"schema": "filees.repository/v2", "repo_id": "a53c17e1-5f6a-5591-bd0b-17820c4344b2",
				"owner_realm_id": "5b2b2595-312c-4e8f-9407-148e2a174033",
				"display_name":   "ARCHIWUM", "state": "initializing",
			},
			wantErr: "schema",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serviceWC, repoID, path := writeRepairFixture(t, tc.record)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			publisher := ServicePublisher{ServiceWC: serviceWC}

			if _, _, err := publisher.CompleteCanonicalRecord(repoID, "svn+ssh://_filees-data@spot.example.net/"); err == nil {
				t.Fatal("incomplete record was accepted")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to name %s", err, tc.wantErr)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("a refused repair still rewrote the record: %s", after)
			}
		})
	}
}
