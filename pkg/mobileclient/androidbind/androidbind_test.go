package androidbind

import "testing"

func TestSaveAndLoadManifestJSONRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	empty, err := s.LoadManifestJSON("repo-1")
	if err != nil || empty != "" {
		t.Fatalf("empty cache: json=%q err=%v", empty, err)
	}

	manifestJSON := `{"schema":"filees.mobile-manifest/v2","repo_id":"repo-1","view_generation":1,"repo_revision":5,"complete":true}`
	ok, err := s.SaveManifestJSONIfNewer(manifestJSON)
	if err != nil || !ok {
		t.Fatalf("save: ok=%v err=%v", ok, err)
	}

	got, err := s.LoadManifestJSON("repo-1")
	if err != nil || got == "" {
		t.Fatalf("load after save: json=%q err=%v", got, err)
	}

	// Older revision must be rejected.
	older := `{"schema":"filees.mobile-manifest/v2","repo_id":"repo-1","view_generation":1,"repo_revision":4,"complete":true}`
	if ok, err := s.SaveManifestJSONIfNewer(older); err == nil || ok {
		t.Fatalf("expected rollback rejection: ok=%v err=%v", ok, err)
	}
}
