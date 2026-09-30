package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// manual/assets/man is generated from docs/man by tools/manual-man-pages.
// Each page records the SHA-256 of its source, so an edited page that was not
// regenerated fails here without mandoc on the test host.
func TestHTMLManPagesAreRegenerated(t *testing.T) {
	const regenerate = "run: go run ./tools/manual-man-pages"
	out := filepath.Join("..", "manual", "assets", "man")
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatalf("%v; %s", err, regenerate)
	}
	want := map[string]bool{"index.html": true}
	for _, source := range manPages(t) {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		rel := filepath.ToSlash(strings.TrimPrefix(source, filepath.Join("..")+string(filepath.Separator)))
		base := filepath.Base(source)
		name := base + ".html"
		want[name] = true
		page, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Errorf("%s has no HTML page; %s", rel, regenerate)
			continue
		}
		if !strings.Contains(string(page), rel+" sha256:"+hex.EncodeToString(sum[:])) {
			t.Errorf("%s changed after %s was generated; %s", rel, name, regenerate)
		}
		if !strings.Contains(string(index), `href="`+name+`"`) {
			t.Errorf("index.html does not list %s; %s", name, regenerate)
		}
	}
	files, err := filepath.Glob(filepath.Join(out, "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !want[filepath.Base(file)] {
			t.Errorf("%s has no source in docs/man; %s", file, regenerate)
		}
	}
}

// Every link between manual pages and from the Tech extract resolves.
func TestHTMLManPageLinksResolve(t *testing.T) {
	href := regexp.MustCompile(`href="([^"#:]+)(#[^"]*)?"`)
	pages, err := filepath.Glob(filepath.Join("..", "manual", "assets", "man", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	pages = append(pages, filepath.Join("..", "manual", "assets", "en", "man-pages.html"), filepath.Join("..", "manual", "assets", "pl", "man-pages.html"))
	for _, page := range pages {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range href.FindAllStringSubmatch(string(data), -1) {
			target := filepath.Join(filepath.Dir(page), filepath.FromSlash(match[1]))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links to missing %s", page, match[1])
			}
		}
	}
}
