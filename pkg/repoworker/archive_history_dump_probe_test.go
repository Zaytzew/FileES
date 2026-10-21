//go:build native_archive_probe

package repoworker

// Test-only inverse of a complete, namespace-isolated import. This is NOT an
// archive API or a general untrusted-dump migration tool. Reuse the production
// bounded record-header reader, and never interpret file bytes as headers.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type archiveProbeProperty struct {
	name    string
	value   []byte
	deleted bool
}

func archiveProbeReadProperties(raw []byte) ([]archiveProbeProperty, error) {
	r := bufio.NewReader(bytes.NewReader(raw))
	var result []archiveProbeProperty
	seen := map[string]bool{}
	readPart := func(line, prefix string) ([]byte, error) {
		if !strings.HasPrefix(line, prefix) {
			return nil, errors.New("invalid property framing")
		}
		n, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
		if err != nil || n < 0 || n > 1<<20 {
			return nil, errors.New("invalid property length")
		}
		b := make([]byte, n+1)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		if b[n] != '\n' {
			return nil, errors.New("missing property separator")
		}
		return b[:n], nil
	}
	for {
		line, err := readDumpLine(r)
		if err != nil {
			return nil, err
		}
		if line == "PROPS-END" {
			if _, err := r.ReadByte(); err != io.EOF {
				return nil, errors.New("trailing property bytes")
			}
			return result, nil
		}
		deleted := strings.HasPrefix(line, "D ")
		prefix := "K "
		if deleted {
			prefix = "D "
		}
		name, err := readPart(line, prefix)
		if err != nil {
			return nil, err
		}
		if len(name) == 0 || seen[string(name)] {
			return nil, errors.New("duplicate or empty property")
		}
		seen[string(name)] = true
		p := archiveProbeProperty{name: string(name), deleted: deleted}
		if !deleted {
			line, err = readDumpLine(r)
			if err != nil {
				return nil, err
			}
			p.value, err = readPart(line, "V ")
			if err != nil {
				return nil, err
			}
		}
		result = append(result, p)
	}
}

func archiveProbeProperties(props []archiveProbeProperty) []byte {
	var b bytes.Buffer
	for _, p := range props {
		if p.deleted {
			fmt.Fprintf(&b, "D %d\n%s\n", len(p.name), p.name)
			continue
		}
		fmt.Fprintf(&b, "K %d\n%s\nV %d\n", len(p.name), p.name, len(p.value))
		b.Write(p.value)
		b.WriteByte('\n')
	}
	b.WriteString("PROPS-END\n")
	return b.Bytes()
}

func archiveProbeWriteRecord(w io.Writer, headers map[string]string, body []byte) error {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s: %s\n", k, headers[k]); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

func archiveProbeStripPrefix(path, prefix string) (string, error) {
	if path == prefix {
		return "", nil
	}
	if !strings.HasPrefix(path, prefix+"/") {
		return "", errors.New("path escapes imported project")
	}
	return strings.TrimPrefix(path, prefix+"/"), nil
}

func archiveProbeMergeinfo(raw []byte, prefix string, inverse map[int64]int64) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	// Mergeinfo syntax is parsed semantically; other properties and file bytes
	// are never newline-normalised by this probe.
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		i := strings.LastIndexByte(line, ':')
		if i < 1 || line[0] != '/' {
			return nil, errors.New("invalid mergeinfo")
		}
		path := line[:i]
		if prefix != "" {
			rel, err := archiveProbeStripPrefix(strings.TrimPrefix(path, "/"), prefix)
			if err != nil {
				return nil, err
			}
			path = "/" + rel
		}
		var ranges []string
		for _, token := range strings.Split(line[i+1:], ",") {
			star := strings.HasSuffix(token, "*")
			token = strings.TrimSuffix(token, "*")
			parts := strings.Split(token, "-")
			if len(parts) > 2 {
				return nil, errors.New("invalid merge range")
			}
			lo, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || lo <= 0 {
				return nil, fmt.Errorf("invalid merge revision %q in %q", parts[0], raw)
			}
			hi := lo
			if len(parts) == 2 {
				hi, err = strconv.ParseInt(parts[1], 10, 64)
			}
			if err != nil || hi < lo || hi-lo > 10000 {
				return nil, errors.New("invalid fixture merge range")
			}
			if inverse != nil {
				previous := int64(-1)
				for rev := lo; rev <= hi; rev++ {
					mapped, ok := inverse[rev]
					if !ok || mapped <= 0 || (previous >= 0 && mapped != previous+1) {
						return nil, errors.New("merge range crosses unmapped history")
					}
					previous = mapped
				}
				lo, hi = inverse[lo], inverse[hi]
			}
			s := strconv.FormatInt(lo, 10)
			if lo != hi {
				s += "-" + strconv.FormatInt(hi, 10)
			}
			if star {
				s += "*"
			}
			ranges = append(ranges, s)
		}
		lines = append(lines, path+":"+strings.Join(ranges, ","))
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n")), nil
}

func archiveProbeExtract(in io.Reader, out io.Writer, prefix, uuid string, rev0 []byte, inverse map[int64]int64, originals map[string][]byte) error {
	if prefix == "" || strings.ContainsAny(prefix, "\r\n\\") {
		return errors.New("invalid fixture prefix")
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid fixture prefix")
		}
	}
	if _, err := archiveProbeReadProperties(rev0); err != nil {
		return err
	}
	if err := archiveProbeWriteRecord(out, map[string]string{"SVN-fs-dump-format-version": "3"}, nil); err != nil {
		return err
	}
	if err := archiveProbeWriteRecord(out, map[string]string{"UUID": uuid}, nil); err != nil {
		return err
	}
	zero := map[string]string{"Revision-number": "0", "Prop-content-length": strconv.Itoa(len(rev0)), "Content-length": strconv.Itoa(len(rev0))}
	if err := archiveProbeWriteRecord(out, zero, rev0); err != nil {
		return err
	}
	r := bufio.NewReader(in)
	include := false
	last := int64(0)
	current := int64(0)
	for {
		headers, err := readDumpHeaders(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		length, err := dumpBodyLength(headers)
		if err != nil {
			return err
		}
		// Only small synthetic fixtures are accepted by this test-only prototype.
		if length > 32<<20 {
			return errors.New("fixture record exceeds probe limit")
		}
		body := make([]byte, int(length))
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		if n, ok := headers["Revision-number"]; ok {
			if _, mixed := headers["Node-path"]; mixed {
				return errors.New("mixed revision and node")
			}
			rev, err := strconv.ParseInt(n, 10, 64)
			if err != nil || rev < 0 {
				return errors.New("invalid revision")
			}
			mapped, ok := inverse[rev]
			include = ok && mapped > 0
			if include {
				if mapped != last+1 {
					return errors.New("incomplete or reordered project history")
				}
				last = mapped
				current = mapped
				headers["Revision-number"] = strconv.FormatInt(mapped, 10)
			}
		} else if _, node := headers["Node-path"]; node && include {
			headers["Node-path"], err = archiveProbeStripPrefix(headers["Node-path"], prefix)
			if err != nil {
				return err
			}
			if cp, ok := headers["Node-copyfrom-path"]; ok {
				headers["Node-copyfrom-path"], err = archiveProbeStripPrefix(cp, prefix)
				if err != nil {
					return err
				}
				rev, parseErr := strconv.ParseInt(headers["Node-copyfrom-rev"], 10, 64)
				mapped, found := inverse[rev]
				if parseErr != nil || !found {
					return errors.New("copy references unmapped revision")
				}
				headers["Node-copyfrom-rev"] = strconv.FormatInt(mapped, 10)
			}
			if count, ok := headers["Prop-content-length"]; ok {
				n, err := strconv.Atoi(count)
				if err != nil || n < 0 || n > len(body) {
					return errors.New("invalid properties")
				}
				props, err := archiveProbeReadProperties(body[:n])
				if err != nil {
					return err
				}
				for i := range props {
					if props[i].name == "svn:mergeinfo" && !props[i].deleted {
						props[i].value, err = archiveProbeMergeinfo(props[i].value, prefix, inverse)
						if err != nil {
							return err
						}
						if originals != nil {
							original, ok := originals[fmt.Sprintf("%d:%s", current, headers["Node-path"])]
							if !ok {
								return errors.New("missing original mergeinfo receipt")
							}
							canonical, err := archiveProbeMergeinfo(original, "", nil)
							if err != nil || !bytes.Equal(canonical, props[i].value) {
								return errors.New("original mergeinfo does not match inverse mapping")
							}
							props[i].value = original
						}
					}
				}
				changed := archiveProbeProperties(props)
				body = append(changed, body[n:]...)
				headers["Prop-content-length"] = strconv.Itoa(len(changed))
				headers["Content-length"] = strconv.Itoa(len(body))
			}
		}
		if include {
			if err := archiveProbeWriteRecord(out, headers, body); err != nil {
				return err
			}
		}
	}
	for _, rev := range inverse {
		if rev > last {
			return errors.New("truncated project history")
		}
	}
	return nil
}

func TestArchiveHistoryProbePropertyFraming(t *testing.T) {
	props := []archiveProbeProperty{{name: "probe:x", value: []byte("\x00\nRevision-number: 987\nNode-path: Other/secret\nPROPS-END\n")}, {name: "gone", deleted: true}}
	raw := archiveProbeProperties(props)
	got, err := archiveProbeReadProperties(raw)
	if err != nil || len(got) != 2 || !bytes.Equal(got[0].value, props[0].value) || !got[1].deleted {
		t.Fatal(got, err)
	}
	for _, bad := range [][]byte{raw[:len(raw)-1], append(append([]byte(nil), raw...), 'x'), []byte("K -1\n"), []byte("K 1\nxXPROPS-END\n")} {
		if _, err := archiveProbeReadProperties(bad); err == nil {
			t.Fatal("malformed properties admitted")
		}
	}
	for _, value := range []string{"/Other/a:3", "/P/a:3-4", "/P/a:0", "/P/a:4-3"} {
		if _, err := archiveProbeMergeinfo([]byte(value), "P", map[int64]int64{3: 1}); err == nil {
			t.Fatal("unsafe mergeinfo", value)
		}
	}
}

func TestArchiveHistoryProbeInverseRefusesUnprovenReferences(t *testing.T) {
	zero := archiveProbeProperties([]archiveProbeProperty{{name: "svn:date", value: []byte("2024-01-01T00:00:00.000000Z")}})
	binary := []byte("\x00\xff\nNode-path: Other/secret\nRevision-number: 999\nPROPS-END\n")
	makeDump := func(path, from, fromRev string, badLength bool) []byte {
		var dump bytes.Buffer
		archiveProbeWriteRecord(&dump, map[string]string{"SVN-fs-dump-format-version": "3"}, nil)
		archiveProbeWriteRecord(&dump, map[string]string{"Revision-number": "3", "Prop-content-length": strconv.Itoa(len(zero)), "Content-length": strconv.Itoa(len(zero))}, zero)
		props := archiveProbeProperties([]archiveProbeProperty{{name: "probe:binary", value: binary}, {name: "svn:mergeinfo", value: []byte("/P/trunk:3*")}})
		body := append(props, binary...)
		headers := map[string]string{"Node-path": path, "Node-kind": "file", "Node-action": "add", "Prop-content-length": strconv.Itoa(len(props)), "Text-content-length": strconv.Itoa(len(binary)), "Content-length": strconv.Itoa(len(body))}
		if from != "" {
			headers["Node-copyfrom-path"] = from
			headers["Node-copyfrom-rev"] = fromRev
		}
		if badLength {
			headers["Content-length"] = "1"
		}
		archiveProbeWriteRecord(&dump, headers, body)
		return dump.Bytes()
	}
	valid := makeDump("P/a", "", "", false)
	var result bytes.Buffer
	if err := archiveProbeExtract(bytes.NewReader(valid), &result, "P", "fixture", zero, map[int64]int64{3: 1}, nil); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(result.Bytes()))
	found := false
	for {
		h, err := readDumpHeaders(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n, err := dumpBodyLength(h)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, int(n))
		if _, err = io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		if h["Node-path"] != "a" {
			continue
		}
		found = true
		pn, _ := strconv.Atoi(h["Prop-content-length"])
		props, err := archiveProbeReadProperties(body[:pn])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body[pn:], binary) || !bytes.Equal(props[0].value, binary) || string(props[1].value) != "/trunk:1*" {
			t.Fatal("body bytes or mergeinfo changed incorrectly")
		}
	}
	if !found {
		t.Fatal("no extracted node")
	}
	for name, input := range map[string][]byte{
		"foreign path":          makeDump("Other/a", "", "", false),
		"prefix boundary":       makeDump("P2/a", "", "", false),
		"foreign copy":          makeDump("P/a", "Other/a", "3", false),
		"unknown copy revision": makeDump("P/a", "P/a", "999", false),
		"body length mismatch":  makeDump("P/a", "", "", true),
		"truncated body":        valid[:len(valid)-20],
		"duplicate header":      bytes.Replace(valid, []byte("Node-path: P/a\n"), []byte("Node-path: P/a\nNode-path: P/b\n"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := archiveProbeExtract(bytes.NewReader(input), &out, "P", "fixture", zero, map[int64]int64{3: 1}, nil); err == nil {
				t.Fatal("unsafe stream accepted")
			}
		})
	}
	for name, mapping := range map[string]map[int64]int64{"missing final revision": {3: 1, 4: 2}, "reordered revision": {3: 2}} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := archiveProbeExtract(bytes.NewReader(valid), &out, "P", "fixture", zero, mapping, nil); err == nil {
				t.Fatal("incomplete mapping accepted")
			}
		})
	}
	for name, receipt := range map[string]map[string][]byte{
		"missing original mergeinfo":      {},
		"inconsistent original mergeinfo": {"1:a": []byte("/trunk:2*")},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := archiveProbeExtract(bytes.NewReader(valid), &out, "P", "fixture", zero, map[int64]int64{3: 1}, receipt); err == nil {
				t.Fatal("unproven mergeinfo receipt accepted")
			}
		})
	}
}

// Independent byte-preserving property oracle directly from native dumps.
// XML tree/content verification remains separate; only mergeinfo is compared
// semantically because prefix/revision translation necessarily changes it.
func archiveProbeDumpMetadata(t *testing.T, dump []byte, prefix string, inverse map[int64]int64, includeZero, canonicalMergeinfo bool) []byte {
	t.Helper()
	result := map[string]map[string]string{}
	r := bufio.NewReader(bytes.NewReader(dump))
	selected := false
	rev := int64(-1)
	for {
		h, err := readDumpHeaders(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n, err := dumpBodyLength(h)
		if err != nil || n > 32<<20 {
			t.Fatal("invalid metadata fixture record", err)
		}
		body := make([]byte, int(n))
		if _, err = io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		key := ""
		if number, ok := h["Revision-number"]; ok {
			rev, err = strconv.ParseInt(number, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			selected = true
			if inverse != nil {
				rev, selected = inverse[rev]
			}
			selected = selected && (rev > 0 || includeZero)
			key = fmt.Sprintf("r%d/revprops", rev)
		} else if path, ok := h["Node-path"]; ok && selected {
			if prefix != "" {
				path, err = archiveProbeStripPrefix(path, prefix)
				if err != nil {
					t.Fatal(err)
				}
			}
			key = fmt.Sprintf("r%d/node:%s/%s", rev, path, h["Node-action"])
		}
		if !selected || key == "" {
			continue
		}
		values := map[string]string{}
		if count, ok := h["Prop-content-length"]; ok {
			pn, err := strconv.Atoi(count)
			if err != nil || pn > len(body) {
				t.Fatal("bad property fixture", err)
			}
			props, err := archiveProbeReadProperties(body[:pn])
			if err != nil {
				t.Fatal(err)
			}
			for _, prop := range props {
				if prop.deleted {
					values[prop.name] = "DELETE"
					continue
				}
				if prop.name == "svn:mergeinfo" && canonicalMergeinfo {
					prop.value, err = archiveProbeMergeinfo(prop.value, prefix, inverse)
					if err != nil {
						t.Fatal(err)
					}
				}
				values[prop.name] = base64.StdEncoding.EncodeToString(prop.value)
			}
		}
		result[key] = values
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func archiveProbeOriginalMergeinfo(t *testing.T, dump []byte) map[string][]byte {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(dump))
	result := map[string][]byte{}
	rev := ""
	for {
		h, err := readDumpHeaders(r)
		if err == io.EOF {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		n, err := dumpBodyLength(h)
		if err != nil || n > 32<<20 {
			t.Fatal("invalid fixture", err)
		}
		body := make([]byte, int(n))
		if _, err = io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		if v, ok := h["Revision-number"]; ok {
			rev = v
			continue
		}
		path, node := h["Node-path"]
		count, props := h["Prop-content-length"]
		if !node || !props {
			continue
		}
		pn, err := strconv.Atoi(count)
		if err != nil || pn > len(body) {
			t.Fatal("bad property record", err)
		}
		values, err := archiveProbeReadProperties(body[:pn])
		if err != nil {
			t.Fatal(err)
		}
		for _, prop := range values {
			if prop.name == "svn:mergeinfo" && !prop.deleted {
				result[rev+":"+path] = append([]byte(nil), prop.value...)
			}
		}
	}
}
