//go:build native_archive_probe

package repoworker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type archiveProbeTools struct {
	t         *testing.T
	bin, root string
}
type archiveProbeSource struct {
	path, uuid string
	head       int64
	zero       []byte
	mergeinfo  map[string][]byte
	snapshots  [][]byte
}
type archiveProbeImport struct {
	source      archiveProbeSource
	prefix      string
	inverse     map[int64]int64
	receipt     string
	receiptHash [32]byte
}
type archiveProbeReceipt struct {
	UUID              string
	Prefix            string
	RevZero           []byte
	Inverse           map[int64]int64
	OriginalMergeinfo map[string][]byte
}

func archiveProbeNative(t *testing.T) archiveProbeTools {
	t.Helper()
	bin := os.Getenv("FILEES_ARCHIVE_PROBE_BIN")
	if bin == "" {
		t.Skip("set FILEES_ARCHIVE_PROBE_BIN to explicit local SVN tool directory")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("absolute tool directory required")
	}
	return archiveProbeTools{t: t, bin: bin, root: t.TempDir()}
}

func (p archiveProbeTools) tool(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(p.bin, name)
}

func (p archiveProbeTools) exec(name string, input []byte, args ...string) ([]byte, error) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.t.Context(), 45*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, p.tool(name), args...)
	c.Dir = p.root
	c.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "SVN_SSH=", "SVN_EDITOR=")
	c.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s %v: %w: %s", name, args, err, stderr.String())
	}
	return out, nil
}

func (p archiveProbeTools) run(name string, input []byte, args ...string) []byte {
	p.t.Helper()
	out, err := p.exec(name, input, args...)
	if err != nil {
		p.t.Fatal(err)
	}
	return out
}

func (p archiveProbeTools) svn(args ...string) []byte {
	p.t.Helper()
	return p.run("svn", nil, append([]string{"--non-interactive", "--no-auth-cache", "--config-dir", filepath.Join(p.root, "client-config")}, args...)...)
}

func archiveProbeURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func (p archiveProbeTools) write(path string, content []byte) {
	p.t.Helper()
	rel, err := filepath.Rel(p.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		p.t.Fatal("fixture write escapes own temporary root")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		p.t.Fatal(err)
	}
}

func (p archiveProbeTools) head(repo string) int64 {
	p.t.Helper()
	n, err := strconv.ParseInt(strings.TrimSpace(string(p.run("svnlook", nil, "youngest", repo))), 10, 64)
	if err != nil {
		p.t.Fatal(err)
	}
	return n
}

func (p archiveProbeTools) dump(repo string, head int64) []byte {
	p.t.Helper()
	return p.run("svnadmin", nil, "dump", "--quiet", "--deltas", "-r", "0:"+strconv.FormatInt(head, 10), repo)
}

func archiveProbeZero(t *testing.T, dump []byte) []byte {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(dump))
	for {
		h, e := readDumpHeaders(r)
		if e != nil {
			t.Fatal(e)
		}
		n, e := dumpBodyLength(h)
		if e != nil {
			t.Fatal(e)
		}
		b := make([]byte, int(n))
		if _, e = io.ReadFull(r, b); e != nil {
			t.Fatal(e)
		}
		if h["Revision-number"] == "0" {
			return b
		}
	}
}

func (p archiveProbeTools) source(name string, year int, emptyHead bool) archiveProbeSource {
	p.t.Helper()
	repo, wc := filepath.Join(p.root, name+"-repo"), filepath.Join(p.root, name+"-wc")
	p.run("svnadmin", nil, "create", repo)
	p.svn("checkout", "--quiet", archiveProbeURI(repo), wc)
	trunk := filepath.Join(wc, "trunk")
	p.write(filepath.Join(trunk, "dir", "document.txt"), []byte("original "+name+"\n"))
	p.write(filepath.Join(trunk, "live.txt"), []byte("unchanged live "+name+"\n"))
	bin := bytes.Repeat([]byte("\x00\xff\nRevision-number: 987\nNode-path: Other/secret\nPROPS-END\n"), 4096)
	p.write(filepath.Join(trunk, "dane Ω.bin"), bin)
	p.write(filepath.Join(trunk, "empty.file"), nil)
	if err := os.Mkdir(filepath.Join(trunk, "empty-dir"), 0700); err != nil {
		p.t.Fatal(err)
	}
	p.svn("add", "--quiet", trunk)
	p.svn("propset", "--quiet", "probe:root", "original "+name, wc)
	lockProperty := filepath.Join(p.root, name+"-needs-lock-property")
	p.write(lockProperty, []byte("*"))
	p.svn("propset", "--quiet", "svn:needs-lock", "-F", lockProperty, filepath.Join(trunk, "empty.file"))
	p.svn("commit", "--quiet", "-m", "create", wc) // r1
	p.svn("update", "--quiet", wc)
	p.svn("copy", "--quiet", trunk, filepath.Join(wc, "branch"))
	p.svn("propset", "--quiet", "svn:mergeinfo", "/trunk:1", filepath.Join(wc, "branch"))
	property := filepath.Join(p.root, name+"-binary-property")
	p.write(property, []byte("\x00\xff\nNode-path: untouched\n"))
	p.svn("propset", "--quiet", "probe:binary", "-F", property, filepath.Join(trunk, "live.txt"))
	p.svn("commit", "--quiet", "-m", "copy and properties", wc) // r2
	p.svn("update", "--quiet", wc)
	p.svn("move", "--quiet", filepath.Join(wc, "branch", "dir"), filepath.Join(wc, "branch", "renamed"))
	p.svn("delete", "--quiet", filepath.Join(trunk, "dir", "document.txt"))
	p.write(filepath.Join(trunk, "dane Ω.bin"), append(append([]byte(nil), bin...), []byte("binary edit "+name)...))
	p.svn("propset", "--quiet", "svn:mergeinfo", "/branch:2*\n/trunk:1-2", wc)
	p.svn("commit", "--quiet", "-m", "rename delete binary", wc) // r3
	p.svn("update", "--quiet", wc)
	p.svn("propdel", "--quiet", "probe:root", wc)
	p.svn("propset", "--quiet", "probe:root", "changed "+name, wc)
	p.svn("propdel", "--quiet", "probe:binary", filepath.Join(trunk, "live.txt"))
	p.write(property, []byte("Zażółć gęślą jaźń"))
	p.svn("propset", "--quiet", "probe:text", "-F", property, filepath.Join(trunk, "live.txt"))
	p.svn("commit", "--quiet", "-m", "property only", wc) // r4
	// A genuine empty revision, not a property-only change disguised as empty.
	props := archiveProbeProperties([]archiveProbeProperty{{name: "svn:log", value: []byte("empty revision")}})
	var empty bytes.Buffer
	archiveProbeWriteRecord(&empty, map[string]string{"SVN-fs-dump-format-version": "2"}, nil)
	archiveProbeWriteRecord(&empty, map[string]string{"Revision-number": "5", "Prop-content-length": strconv.Itoa(len(props)), "Content-length": strconv.Itoa(len(props))}, props)
	p.run("svnadmin", empty.Bytes(), "load", "--quiet", "--ignore-uuid", repo)
	p.svn("update", "--quiet", wc)
	p.svn("copy", "--quiet", filepath.Join(wc, "branch", "renamed"), filepath.Join(trunk, "restored"))
	p.svn("propset", "--quiet", "svn:mergeinfo", "/branch:2-5*\n/trunk:1-5", wc)
	p.svn("commit", "--quiet", "-m", "copy from older branch", wc) // r6
	if emptyHead {
		p.svn("update", "--quiet", wc)
		p.svn("delete", "--quiet", trunk, filepath.Join(wc, "branch"))
		p.svn("propdel", "--quiet", "svn:mergeinfo", wc)
		p.svn("propdel", "--quiet", "probe:root", wc)
		p.svn("commit", "--quiet", "-m", "empty HEAD retains history", wc) // r7
	}
	head := p.head(repo)
	for rev := int64(0); rev <= head; rev++ {
		for key, value := range map[string]string{
			"svn:author":     "fixture-" + name + "-作者",
			"svn:date":       fmt.Sprintf("%04d-01-%02dT12:00:00.000000Z", year, rev+1),
			"svn:log":        fmt.Sprintf("%s original r%d\nRevision-number: 777\n", name, rev),
			"probe:revision": fmt.Sprintf("%s/%d", name, rev),
		} {
			file := filepath.Join(p.root, "property-value")
			p.write(file, []byte(value))
			p.run("svnadmin", nil, "setrevprop", repo, "-r", strconv.FormatInt(rev, 10), key, file)
		}
	}
	s := archiveProbeSource{path: repo, uuid: strings.TrimSpace(string(p.run("svnlook", nil, "uuid", repo))), head: head}
	dump := p.dump(repo, head)
	s.zero = archiveProbeZero(p.t, dump)
	s.mergeinfo = archiveProbeOriginalMergeinfo(p.t, dump)
	for rev := int64(0); rev <= head; rev++ {
		s.snapshots = append(s.snapshots, p.snapshot(repo, rev, "", nil))
	}
	p.t.Logf("source %s: r0:r%d, empty-HEAD=%t, binary=%d B", name, head, emptyHead, len(bin))
	return s
}

type archiveProbeXMLProperty struct {
	Name     string `xml:"name,attr"`
	Encoding string `xml:"encoding,attr"`
	Value    string `xml:",chardata"`
}
type archiveProbeXMLProperties struct {
	Properties []archiveProbeXMLProperty `xml:"target>property"`
	Revprops   []archiveProbeXMLProperty `xml:"revprops>property"`
}

func (p archiveProbeTools) properties(repo string, rev int64, rel string, revprop bool, prefix string, inverse map[int64]int64) map[string]string {
	p.t.Helper()
	args := []string{"proplist", "--xml", "--verbose", "-r", strconv.FormatInt(rev, 10)}
	if revprop {
		args = append(args, "--revprop")
	}
	path := repo
	if !revprop && rel != "" {
		path = filepath.Join(repo, filepath.FromSlash(rel))
	}
	uri := archiveProbeURI(path)
	if !revprop {
		uri += "@" + strconv.FormatInt(rev, 10)
	}
	args = append(args, uri)
	var document archiveProbeXMLProperties
	if err := xml.Unmarshal(p.svn(args...), &document); err != nil {
		p.t.Fatal(err)
	}
	props := map[string]string{}
	items := document.Properties
	if revprop {
		items = document.Revprops
		if len(items) == 0 {
			p.t.Fatal("missing native revision properties")
		}
	}
	for _, item := range items {
		value := []byte(item.Value)
		if item.Encoding == "base64" {
			var err error
			value, err = base64.StdEncoding.DecodeString(item.Value)
			if err != nil {
				p.t.Fatal(err)
			}
		}
		if item.Name == "svn:mergeinfo" {
			var err error
			value, err = archiveProbeMergeinfo(value, prefix, inverse)
			if err != nil {
				p.t.Fatal(err)
			}
		}
		props[item.Name] = base64.StdEncoding.EncodeToString(value)
	}
	return props
}

func (p archiveProbeTools) snapshot(repo string, rev int64, prefix string, inverse map[int64]int64) []byte {
	p.t.Helper()
	uri := archiveProbeURI(repo)
	if prefix != "" {
		uri += "/" + prefix
	}
	var tree struct {
		Entries []struct {
			Name string `xml:"name"`
			Kind string `xml:"kind,attr"`
		} `xml:"list>entry"`
	}
	if err := xml.Unmarshal(p.svn("list", "--xml", "--recursive", "-r", strconv.FormatInt(rev, 10), uri+"@"+strconv.FormatInt(rev, 10)), &tree); err != nil {
		p.t.Fatal(err)
	}
	tree.Entries = append(tree.Entries, struct {
		Name string `xml:"name"`
		Kind string `xml:"kind,attr"`
	}{Kind: "dir"})
	kinds := map[string]string{}
	properties := map[string]map[string]string{}
	contents := map[string]string{}
	for _, entry := range tree.Entries {
		rel, path := entry.Name, entry.Name
		if prefix != "" {
			path = prefix
			if rel != "" {
				path += "/" + rel
			}
		}
		kinds[rel] = entry.Kind
		properties[rel] = p.properties(repo, rev, path, false, prefix, inverse)
		if entry.Kind == "file" {
			data := p.svn("cat", "-r", strconv.FormatInt(rev, 10), archiveProbeURI(filepath.Join(repo, filepath.FromSlash(path)))+"@"+strconv.FormatInt(rev, 10))
			hash := sha256.Sum256(data)
			contents[rel] = hex.EncodeToString(hash[:])
		}
	}
	var changes []string
	if rev > 0 {
		var log struct {
			Entries []struct {
				Paths []struct {
					Path    string `xml:",chardata"`
					Action  string `xml:"action,attr"`
					Kind    string `xml:"kind,attr"`
					From    string `xml:"copyfrom-path,attr"`
					FromRev string `xml:"copyfrom-rev,attr"`
				} `xml:"paths>path"`
			} `xml:"logentry"`
		}
		if err := xml.Unmarshal(p.svn("log", "--xml", "--verbose", "-r", strconv.FormatInt(rev, 10), archiveProbeURI(repo)), &log); err != nil {
			p.t.Fatal(err)
		}
		for _, entry := range log.Entries {
			for _, item := range entry.Paths {
				path := strings.Trim(item.Path, "/")
				if prefix != "" {
					var err error
					path, err = archiveProbeStripPrefix(path, prefix)
					if err != nil {
						p.t.Fatal(err)
					}
				}
				change := item.Action + ":" + item.Kind + ":" + path
				if item.From != "" {
					from := strings.Trim(item.From, "/")
					if prefix != "" {
						var err error
						from, err = archiveProbeStripPrefix(from, prefix)
						if err != nil {
							p.t.Fatal(err)
						}
					}
					n, err := strconv.ParseInt(item.FromRev, 10, 64)
					if err != nil {
						p.t.Fatal(err)
					}
					if inverse != nil {
						mapped, ok := inverse[n]
						if !ok {
							p.t.Fatal("unmapped copy", n)
						}
						n = mapped
					}
					change += fmt.Sprintf(":copy:%s@%d", from, n)
				}
				changes = append(changes, change)
			}
		}
	}
	sort.Strings(changes)
	state := map[string]any{"tree": kinds, "properties": properties, "sha256": contents, "changes": changes, "revprops": p.properties(repo, rev, "", true, "", nil)}
	data, err := json.Marshal(state)
	if err != nil {
		p.t.Fatal(err)
	}
	return data
}

func (p archiveProbeTools) importSource(target string, s archiveProbeSource, prefix string) archiveProbeImport {
	p.t.Helper()
	// These are owned file:/// fixtures only. Never accept an existing project.
	if _, err := p.exec("svnlook", nil, "tree", target, prefix); err == nil {
		p.t.Fatal("project prefix already exists")
	}
	p.svn("mkdir", "--quiet", "-m", "technical project root", archiveProbeURI(target)+"/"+prefix)
	base := p.head(target)
	zeroBefore := p.snapshot(target, 0, "", nil)
	p.run("svnadmin", p.dump(s.path, s.head), "load", "--quiet", "--ignore-uuid", "--parent-dir", prefix, target)
	if got := p.head(target); got != base+s.head {
		p.t.Fatalf("lost empty revision: HEAD %d, expected %d", got, base+s.head)
	}
	if !bytes.Equal(zeroBefore, p.snapshot(target, 0, "", nil)) {
		p.t.Fatal("source r0 overwrote aggregate r0")
	}
	inverse := map[int64]int64{base: 0}
	for r := int64(1); r <= s.head; r++ {
		inverse[base+r] = r
	}
	p.t.Logf("import %s: source r1:r%d -> aggregate r%d:r%d", prefix, s.head, base+1, base+s.head)
	receipt := archiveProbeReceipt{UUID: s.uuid, Prefix: prefix, RevZero: s.zero, Inverse: inverse, OriginalMergeinfo: s.mergeinfo}
	raw, err := json.Marshal(receipt)
	if err != nil {
		p.t.Fatal(err)
	}
	name := filepath.Join(p.root, prefix+"-provenance.json")
	p.write(name, raw)
	return archiveProbeImport{source: s, prefix: prefix, inverse: inverse, receipt: name, receiptHash: sha256.Sum256(raw)}
}

func (p archiveProbeTools) verifyImport(target string, imported archiveProbeImport) {
	p.t.Helper()
	wantMetadata := archiveProbeDumpMetadata(p.t, p.dump(imported.source.path, imported.source.head), "", nil, false, true)
	gotMetadata := archiveProbeDumpMetadata(p.t, p.dump(target, p.head(target)), imported.prefix, imported.inverse, false, true)
	if !bytes.Equal(wantMetadata, gotMetadata) {
		p.t.Fatalf("raw property metadata changed for %s\nwant %s\ngot %s", imported.prefix, wantMetadata, gotMetadata)
	}
	for targetRev, sourceRev := range imported.inverse {
		if sourceRev == 0 {
			continue
		}
		got := p.snapshot(target, targetRev, imported.prefix, imported.inverse)
		if !bytes.Equal(got, imported.source.snapshots[sourceRev]) {
			p.t.Fatalf("history mismatch %s source r%d / target r%d\nwant %s\ngot  %s", imported.prefix, sourceRev, targetRev, imported.source.snapshots[sourceRev], got)
		}
	}
	p.t.Logf("verified %s: all %d revisions, tree, SHA-256, node/revision properties and copyfrom", imported.prefix, imported.source.head)
}

func TestArchiveHistoryNativeRoundTripAndAppend(t *testing.T) {
	p := archiveProbeNative(t)
	for _, name := range []string{"svn", "svnadmin", "svnlook", "svnrdump"} {
		t.Log(name, strings.TrimSpace(string(p.run(name, nil, "--version", "--quiet"))))
	}
	a, b, c := p.source("alpha", 2025, false), p.source("beta", 2024, false), p.source("gamma", 2023, true)
	target := filepath.Join(p.root, "aggregate")
	p.run("svnadmin", nil, "create", target)
	imports := []archiveProbeImport{p.importSource(target, a, "Alpha"), p.importSource(target, b, "Beta")}
	for _, item := range imports {
		p.verifyImport(target, item)
	}
	oldHead := p.head(target)
	oldDump := p.dump(target, oldHead)
	candidate := filepath.Join(p.root, "private-candidate")
	p.run("svnadmin", nil, "hotcopy", target, candidate)
	uuid := strings.TrimSpace(string(p.run("svnlook", nil, "uuid", target)))
	if string(p.run("svnlook", nil, "uuid", candidate)) != string(p.run("svnlook", nil, "uuid", target)) {
		t.Fatal("hotcopy UUID changed")
	}
	// Existing partial WC against this test candidate; append is serial here,
	// NOT proof of production atomic cutover or a server session fence.
	wc := filepath.Join(p.root, "existing-partial-wc")
	p.svn("checkout", "--quiet", "--depth", "empty", archiveProbeURI(candidate), wc)
	p.svn("update", "--quiet", "--set-depth", "infinity", filepath.Join(wc, "Alpha"))
	edit := filepath.Join(wc, "Alpha", "trunk", "live.txt")
	local := []byte("preserve unpublished local edit\n")
	p.write(edit, local)
	p.write(filepath.Join(wc, "Alpha", "local-only.bak"), []byte("preserve unversioned data"))
	imports = append(imports, p.importSource(candidate, c, "Gamma"))
	if !bytes.Equal(oldDump, p.dump(candidate, oldHead)) {
		t.Fatal("append changed old revision dump")
	}
	if !bytes.Equal(oldDump, p.dump(target, oldHead)) {
		t.Fatal("private candidate modified original aggregate")
	}
	if strings.TrimSpace(string(p.run("svnlook", nil, "uuid", candidate))) != uuid {
		t.Fatal("append UUID changed")
	}
	p.svn("update", "--quiet", wc)
	got, err := os.ReadFile(edit)
	if err != nil || !bytes.Equal(got, local) {
		t.Fatal("update lost local edit", err)
	}
	if _, err := os.Stat(filepath.Join(wc, "Alpha", "local-only.bak")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wc, "Gamma")); !os.IsNotExist(err) {
		t.Fatal("sparse root unexpectedly expanded itself", err)
	}
	if got := strings.TrimSpace(string(p.svn("info", "--show-item", "revision", wc))); got != strconv.FormatInt(p.head(candidate), 10) {
		t.Fatal("existing WC did not advance", got)
	}
	p.svn("update", "--quiet", "--depth", "empty", filepath.Join(wc, "Gamma"))
	if _, err := os.Stat(filepath.Join(wc, "Gamma")); err != nil {
		t.Fatal("explicit partial fetch failed", err)
	}
	for _, item := range imports {
		p.verifyImport(candidate, item)
	}
	p.run("svnadmin", nil, "verify", "--quiet", candidate)
	full := p.dump(candidate, p.head(candidate))
	for _, item := range imports {
		var extracted bytes.Buffer
		raw, err := os.ReadFile(item.receipt)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(raw) != item.receiptHash {
			t.Fatal("provenance changed")
		}
		var receipt archiveProbeReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		if err := archiveProbeExtract(bytes.NewReader(full), &extracted, receipt.Prefix, receipt.UUID, receipt.RevZero, receipt.Inverse, receipt.OriginalMergeinfo); err != nil {
			t.Fatal(err)
		}
		restored := filepath.Join(p.root, "extracted-"+item.prefix)
		p.run("svnadmin", nil, "create", restored)
		p.run("svnadmin", extracted.Bytes(), "load", "--quiet", "--ignore-uuid", restored)
		p.run("svnadmin", nil, "verify", "--quiet", restored)
		originalMetadata := archiveProbeDumpMetadata(t, p.dump(item.source.path, item.source.head), "", nil, true, false)
		restoredMetadata := archiveProbeDumpMetadata(t, p.dump(restored, p.head(restored)), "", nil, true, false)
		if !bytes.Equal(originalMetadata, restoredMetadata) {
			t.Fatal("extraction changed raw property/revprop bytes", item.prefix)
		}
		if p.head(restored) != item.source.head {
			t.Fatal("extracted revision count differs")
		}
		for rev := int64(0); rev <= item.source.head; rev++ {
			if got := p.snapshot(restored, rev, "", nil); !bytes.Equal(got, item.source.snapshots[rev]) {
				t.Fatalf("extracted %s r%d differs\nwant %s\ngot %s", item.prefix, rev, item.source.snapshots[rev], got)
			}
			if got := p.snapshot(item.source.path, rev, "", nil); !bytes.Equal(got, item.source.snapshots[rev]) {
				t.Fatal("source was changed")
			}
		}
		t.Logf("round-trip %s: r0:r%d identical; source unchanged", item.prefix, item.source.head)
	}
	// A damaged import commits its preceding revisions: verify alone can pass
	// on an incomplete staging repository. It must never become the result.
	failed := filepath.Join(p.root, "incomplete-candidate")
	p.run("svnadmin", nil, "hotcopy", target, failed)
	p.svn("mkdir", "--quiet", "-m", "technical incomplete root", archiveProbeURI(failed)+"/Incomplete")
	damaged := p.dump(c.path, c.head)
	if _, err := p.exec("svnadmin", damaged[:len(damaged)-30], "load", "--quiet", "--ignore-uuid", "--parent-dir", "Incomplete", failed); err == nil {
		t.Fatal("truncated native import succeeded")
	}
	if head := p.head(failed); head <= oldHead+1 || head >= oldHead+1+c.head {
		t.Fatal("fixture did not expose partial committed import", head)
	}
	p.run("svnadmin", nil, "verify", "--quiet", failed)
	if !bytes.Equal(oldDump, p.dump(target, oldHead)) {
		t.Fatal("incomplete staging damaged original aggregate")
	}
	t.Log("negative: truncated load left a valid but incomplete private candidate; original aggregate unchanged")
	// Same UUID and HEAD still do not prove preservation of old history.
	tampered := filepath.Join(p.root, "tampered-candidate")
	p.run("svnadmin", nil, "hotcopy", target, tampered)
	value := filepath.Join(p.root, "tampered-revprop")
	p.write(value, []byte("unexpected historical log change"))
	p.run("svnadmin", nil, "setrevprop", tampered, "-r", "2", "svn:log", value)
	if p.head(tampered) != oldHead || strings.TrimSpace(string(p.run("svnlook", nil, "uuid", tampered))) != uuid {
		t.Fatal("tamper fixture changed identity")
	}
	if bytes.Equal(oldDump, p.dump(tampered, oldHead)) {
		t.Fatal("old-history comparison missed revprop alteration")
	}
	t.Log("negative: altered old revprop detected despite unchanged UUID and HEAD")
	t.Log("PASS history/append/partial-WC continuity; no archive policy, async worker or atomic publication claimed")
}
