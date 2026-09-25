package commit

import "testing"

// Owner's production, 2026-09-25: working copies inside the Nextcloud folder
// (virtual files) lost every new file from publication, because Nextcloud
// turns each saved file into its own placeholder within seconds and the add
// path took any provider's placeholder for an anchor's.
func TestPlaceholderAddDecision(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		anchored, placeholder, notOnDisk bool
		want                             placeholderAddAction
	}{
		{"ordinary file", false, false, false, placeholderAddIt},
		{"Nextcloud placeholder with its bytes on disk", false, true, false, placeholderAddIt},
		{"Nextcloud placeholder freed from this disk", false, true, true, placeholderWait},
		{"anchor placeholder not opened here", true, true, true, placeholderDrop},
		{"anchor placeholder already fetched", true, true, false, placeholderDrop},
		{"anchored copy, file saved by the person", true, false, false, placeholderAddIt},
	} {
		if got := placeholderAdd(tc.anchored, tc.placeholder, tc.notOnDisk); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
