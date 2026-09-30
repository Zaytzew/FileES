package packaging

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// manPages maps "name(section)" to the page's path under docs/man.
func manPages(t *testing.T) map[string]string {
	t.Helper()
	pages := map[string]string{}
	files, err := filepath.Glob(filepath.Join("..", "docs", "man", "man*", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		base := filepath.Base(file)
		dot := strings.LastIndex(base, ".")
		pages[base[:dot]+"("+base[dot+1:]+")"] = file
	}
	if len(pages) == 0 {
		t.Fatal("no manual pages under docs/man")
	}
	return pages
}

// Every page names itself as its file does, and every FileES page it points
// to exists: a broken Xr is a dead link in the HTML manual as well.
func TestManPagesAreConsistent(t *testing.T) {
	pages := manPages(t)
	dt := regexp.MustCompile(`(?m)^\.Dt (\S+) (\d)$`)
	xr := regexp.MustCompile(`(?m)^\.Xr (filees\S*) (\d)`)
	for name, file := range pages {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		match := dt.FindStringSubmatch(text)
		if match == nil || strings.ToLower(match[1])+"("+match[2]+")" != name {
			t.Errorf("%s: .Dt %v does not match the file name", file, match)
		}
		if filepath.Base(filepath.Dir(file)) != "man"+match[2] {
			t.Errorf("%s: section %s is in the wrong directory", file, match[2])
		}
		for _, ref := range xr.FindAllStringSubmatch(text, -1) {
			if _, ok := pages[ref[1]+"("+ref[2]+")"]; !ok {
				t.Errorf("%s: .Xr %s %s names no page in docs/man", file, ref[1], ref[2])
			}
		}
		if !bytes.HasSuffix(data, []byte("\n")) {
			t.Errorf("%s: no final newline", file)
		}
	}
}

// Every program the signed server release installs has its own page.
func TestEveryInstalledServerProgramHasAManPage(t *testing.T) {
	pages := manPages(t)
	raw, err := os.ReadFile(filepath.Join("server", "openbsd-binary-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Files []struct {
			Source string `json:"source"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	for _, file := range policy.Files {
		if !strings.HasPrefix(file.Source, "bin/") {
			continue
		}
		name := path.Base(file.Source)
		if _, ok := pages[name+"(8)"]; !ok {
			t.Errorf("%s is installed but has no %s(8)", file.Source, name)
		}
	}
}

// mandoc(1) is what OpenBSD renders with. Where it is installed, the pages
// must be free of warnings; a missing cross-reference to a page that is not
// installed on the test host is only a style note and is ignored.
func TestManPagesPassMandocLint(t *testing.T) {
	mandoc, err := exec.LookPath("mandoc")
	if err != nil {
		t.Skip("mandoc not installed")
	}
	pages := manPages(t)
	var files []string
	for _, file := range pages {
		files = append(files, file)
	}
	sort.Strings(files)
	out, _ := exec.Command(mandoc, append([]string{"-T", "lint", "-W", "style"}, files...)...).CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.Contains(line, "referenced manual not found") {
			continue
		}
		t.Error(line)
	}
}
