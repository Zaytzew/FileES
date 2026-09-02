package deploy

import (
	"path/filepath"
	"testing"

	"filees/pkg/clientprofile"
)

// The directory a server's state is created in and the directory its files are
// looked for in must be the same one. They were built independently - one
// through profileStateRoot, the others by writing the join out at the call
// site - and only the first learned that an ID may contain something the
// filesystem reserves.
//
// The result was activation getting past mkdir and then failing on
// "load pinned host key: open ...\servers\atmprojekt:filees\known_hosts", with
// the state directory sitting right there under the name that could exist.
func TestStateRootAndLookupsAgreeOnTheDirectory(t *testing.T) {
	base := t.TempDir()
	const id = "atmprojekt:filees"

	created, err := profileStateRoot(base, ServerProfile{
		ID: id, Address: "cloud.example.net:2222", KnownHostsPath: filepath.Join(base, "known_hosts"),
	})
	if err != nil {
		t.Fatalf("an ordinary identifier must be storable on this platform: %v", err)
	}

	expected, err := clientprofile.ServerDir(base, id)
	if err != nil {
		t.Fatal(err)
	}
	if created != expected {
		t.Fatalf("the directory created (%q) is not the one everything else will look in (%q)", created, expected)
	}
}

// An identifier with nothing reserved in it must keep its directory exactly,
// or every installation in the field would be looking in a new place.
func TestOrdinaryIdentifiersKeepTheirDirectory(t *testing.T) {
	base := t.TempDir()
	created, err := profileStateRoot(base, ServerProfile{
		ID: "spot", Address: "spot.example.net:2223", KnownHostsPath: filepath.Join(base, "known_hosts"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != filepath.Join(base, "spot") {
		t.Fatalf("existing installations must not be moved: %q", created)
	}
}
