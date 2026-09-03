package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAServerIDWithAColonBecomesAWritableName(t *testing.T) {
	segment, err := serverPathSegment("atmprojekt:filees")
	if err != nil {
		t.Fatalf("serverPathSegment: %v", err)
	}
	if strings.ContainsAny(segment, `:\/*?"<>|`) {
		t.Fatalf("segment %q still contains a character Windows reserves", segment)
	}

	// Proven against the filesystem rather than against a character list,
	// because the character list is what the original code effectively assumed
	// and it was wrong. This is the exact name that failed on the owner's
	// machine: renaming onto it returned "The parameter is incorrect" and the
	// deletion of one repository sat unfinished for hours, showing a UID and no
	// actions with no clue why.
	path := filepath.Join(t.TempDir(), "filees-recovery-"+segment+"-4044fa26.fkr")
	if err := os.WriteFile(path, []byte("kit"), 0o600); err != nil {
		t.Fatalf("write recovery kit name: %v", err)
	}
	renamed := path + ".renamed"
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("rename onto recovery kit name: %v", err)
	}
}

func TestAnOrdinaryServerIDIsLeftAlone(t *testing.T) {
	// Identity for anything already in use, so recovery kits written before
	// this keep their names and nothing has to be migrated.
	for _, id := range []string{"spot", "manual", "cloud-01", "atmprojekt.filees"} {
		segment, err := serverPathSegment(id)
		if err != nil {
			t.Fatalf("serverPathSegment(%q): %v", id, err)
		}
		if segment != id {
			t.Errorf("serverPathSegment(%q) = %q; existing names must not move", id, segment)
		}
	}
}

// No path may be assembled from a raw server ID.
//
// The sweep after r762 called the raw-concatenation bug fixed in "the only such
// place". Two sites survived it, both building recovery-kit filenames, and the
// second one surfaced only when a real deletion stalled on the owner's live
// machine. The sweep was reading; this is the mechanical version, so the next
// one costs a test run instead of an afternoon.
func TestNoPathIsBuiltFromARawServerID(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// The fields that carry a server ID. RepoID and operation IDs are UUIDs
	// and safe by construction, so they are deliberately not listed.
	identifiers := []string{"ServerID", "serverID", "RealmID", "realmID"}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, "filepath.Join(") {
				continue
			}
			for _, identifier := range identifiers {
				// The shape of the defect: an ID glued into a path segment
				// with +, rather than encoded first.
				if strings.Contains(line, "+"+identifier) || strings.Contains(line, identifier+"+") ||
					strings.Contains(line, "+ "+identifier) || strings.Contains(line, identifier+" +") {
					t.Errorf("%s:%d builds a path from a raw %s; use serverPathSegment first:\n\t%s",
						name, number+1, identifier, trimmed)
				}
			}
		}
	}
}
