//go:build !windows

package web

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
)

// The files of a ZIP carry the date of the publication the page shows.
func TestBundleFilesAreDatedWithThePublication(t *testing.T) {
	committed := time.Date(2026, 9, 3, 11, 40, 0, 0, time.UTC)
	if got := bundleModTime(channel.Projection{UpdatedAt: committed.Add(-time.Hour), RevisionAt: committed}); !got.Equal(committed) {
		t.Fatalf("bundle date = %s, want the served revision %s", got, committed)
	}
	defined := time.Date(2026, 8, 14, 9, 12, 0, 0, time.UTC)
	if got := bundleModTime(channel.Projection{UpdatedAt: defined}); !got.Equal(defined) {
		t.Fatalf("bundle date = %s, want the share date %s", got, defined)
	}
}

// 1980-01-01 00:00 UTC is the first moment a DOS date can hold; in Warsaw it
// is still 31 December 1979, which readers showed as 30 December 1899. The
// fallback must stay inside the format's range in every time zone.
func TestBundleDateFallbackStaysInsideTheZIPRange(t *testing.T) {
	for _, projection := range []channel.Projection{{}, {UpdatedAt: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}} {
		got := bundleModTime(projection)
		for _, offset := range []int{-12, 0, 14} {
			if local := got.In(time.FixedZone("zone", offset*3600)); local.Year() < 1980 {
				t.Fatalf("fallback %s is %s at UTC%+d, before the ZIP range", got, local, offset)
			}
		}
	}
}

func TestBundleArchiveCarriesOnePublicationDateForEveryFile(t *testing.T) {
	f := newWebFixture(t, nil)
	f.handler.MaxBundleFiles = 10
	f.handler.MaxBundleSize = 1 << 20
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
	request := httptest.NewRequest(http.MethodPost, "https://example.test/atmprojekt/przetarg-2026/bundle?v="+url.QueryEscape(visit), strings.NewReader("all=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	writer := httptest.NewRecorder()
	f.handler.ServeHTTP(writer, request)
	archive, err := zip.NewReader(bytes.NewReader(writer.Body.Bytes()), int64(writer.Body.Len()))
	if err != nil {
		t.Fatalf("bundle is not a ZIP (%d): %v", writer.Code, err)
	}
	if len(archive.File) == 0 {
		t.Fatal("bundle is empty")
	}
	// The fixture defines its share at this clock and dates no revision, so
	// the share date is the publication the page shows.
	want := time.Unix(1700000000, 0).UTC()
	for _, file := range archive.File {
		if !file.Modified.Equal(want) {
			t.Fatalf("%s is dated %s, want the publication %s", file.Name, file.Modified, want)
		}
		if year := 1980 + int(file.ModifiedDate>>9); year != want.Year() {
			t.Fatalf("%s carries DOS year %d, want %d", file.Name, year, want.Year())
		}
	}
}
