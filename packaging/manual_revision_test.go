package packaging_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The guards for manual/ live here, not in manual/ itself.
//
// manual/ is the image of the manual's htdocs: on the server the document root
// is that working copy. These tests sat in manual/manual_revision_test.go from
// r1073, so every deployment carried a Go file into the web root, and
// TestManualHtdocsFitsStaticAllowlist (metadata_test.go, built only off
// Windows) failed on it — noticed by the Linux session on 2026-09-17 after
// Windows runs had never compiled that check.
var manualRoot = filepath.Join("..", "manual")

// handWrittenStamp matches a revision number typed into the page by a human.
//
// Every one of them was correct on the day it was typed and wrong soon after:
// on 2026-09-09 the pages announced "source r1042" while the tree stood at
// r1070 — twenty-eight revisions of drift inside a single day, on a service
// that is publicly reachable.
//
// The first version matched only "source r1042" and missed the same claim
// written as "Source SVN r1042", "signed r1042 on production" and "points at
// release r1042", which stood on the pages for another five days. History
// ("composition has been derived since r601") and command examples
// ("--adopt r1042") do not age and are not matched.
var handWrittenStamp = regexp.MustCompile(`(?i)(source|źródło|zrodlo|svn|signed|podpisan\p{L}*|release|wydani\p{L}*)\s+r\d+`)

const revisionMeta = `name="filees-source-revision"`

func manualPages(t *testing.T) []string {
	t.Helper()
	var pages []string
	err := filepath.WalkDir(manualRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".html") {
			pages = append(pages, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no manual pages found; this guard would pass vacuously")
	}
	return pages
}

// TestManualPagesCarryNoHandWrittenRevision is the whole point: a number a
// human typed is a claim that ages, and nothing was checking it.
func TestManualPagesCarryNoHandWrittenRevision(t *testing.T) {
	for _, page := range manualPages(t) {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		if match := handWrittenStamp.Find(raw); match != nil {
			t.Errorf("%s: hand-written revision stamp %q; use the svn:keywords meta instead, "+
				"which the repository substitutes on every commit of this file", page, match)
		}
	}
}

// TestManualPagesDeclareTheirSourceRevision keeps the machine-readable value
// present.
//
// It is deliberately not rendered: the visible footer names the edition date
// and nothing more, because a revision shown to a reader is a promise about
// freshness that a static page cannot keep on its own. The meta is for
// whoever audits the deployed tree.
func TestManualPagesDeclareTheirSourceRevision(t *testing.T) {
	for _, page := range manualPages(t) {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), revisionMeta) {
			t.Errorf("%s: missing the %s meta", page, revisionMeta)
		}
	}
}

// TestManualIndexMaturityLegendsStayStructurallyEquivalent protects the shared
// four-column layout. A plain paragraph in either edition becomes the first
// grid cell and collapses the entire legend into one narrow column.
func TestManualIndexMaturityLegendsStayStructurallyEquivalent(t *testing.T) {
	for _, page := range []string{"assets/en/index.html", "assets/pl/index.html"} {
		raw, err := os.ReadFile(filepath.Join(manualRoot, page))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(raw), `class="legend-item"`); got != 4 {
			t.Errorf("%s: maturity legend has %d items, want 4", page, got)
		}
	}
}

// TestManualKeywordSubstitutionIsEnabled catches the failure that would make
// the meta useless without making it look broken: the property missing, so the
// page ships the literal text $Rev$ instead of a number.
func TestManualKeywordSubstitutionIsEnabled(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn not in PATH; keyword property cannot be read here")
	}
	if err := exec.Command(svn, "info", "--non-interactive", manualRoot).Run(); err != nil {
		t.Skip("not a working copy; keyword property cannot be read here")
	}
	for _, page := range manualPages(t) {
		out, err := exec.Command(svn, "propget", "svn:keywords", "--non-interactive", page).Output()
		if err != nil || !strings.Contains(string(out), "Rev") {
			t.Errorf("%s: svn:keywords does not include Rev, so the page would ship a literal "+
				"placeholder instead of its revision", page)
		}
	}
}
