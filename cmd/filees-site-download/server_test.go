package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/serverinstall/manifest"
)

func serverRelease(t *testing.T, r *repo, key signer, sequence uint64) string {
	t.Helper()
	id := "r1420-server"
	if sequence == 1 {
		id = "r1-server"
	}
	base := "releases/" + id + "/openbsd-amd64/"
	payload := []byte("OpenBSD binary")
	m := manifest.Manifest{SchemaVersion: 2, ReleaseID: id, Platform: "openbsd-amd64", Sequence: sequence, SecurityEpoch: 1, Files: []manifest.File{
		{Source: "bin/filees-install", Target: "{sbin_dir}/filees-install", Kind: "binary", Owner: "root", Group: "wheel", Mode: "4555", SHA256: digest(payload)},
		{Source: "bin/filees-install", Target: "{libexec_dir}/second-target", Kind: "binary", Owner: "root", Group: "wheel", SHA256: digest(payload)},
	}}
	raw, _ := json.Marshal(m)
	ch, _ := json.Marshal(manifest.Channel{SchemaVersion: 1, ReleaseID: id, Manifest: base + "manifest.json", Sequence: sequence, SecurityEpoch: 1})
	r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = raw, key.sign(raw)
	r.files["channels/alpha.json"], r.files["channels/alpha.json.sig"] = ch, key.sign(ch)
	r.files[base+"bin/filees-install"] = payload
	return base
}

func TestServerPublicationIndependentAndCached(t *testing.T) {
	key := newSigner(t)
	r := &repo{files: map[string][]byte{}, fetched: map[string]int{}}
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	base := serverRelease(t, r, key, 1420)
	p := publisher(t, r, key, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(p.OutDir), 0755); err != nil {
		t.Fatal(err)
	}
	p.Config.Server = &ServerConfig{Channel: "alpha", Platform: "openbsd-amd64"}
	p.Template = append([]byte(testTemplate), []byte(`<h2>{{SERVER_RELEASE_ID}}</h2><a href="{{SERVER_FILE}}">{{SERVER_SHA256}}</a>`)...)
	first, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || len(first.Installers) != 2 {
		t.Fatalf("result: %+v", first)
	}
	archiveName := "FileES-r1420-server-openbsd-amd64.tar.gz"
	data, err := os.ReadFile(filepath.Join(p.OutDir, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	contents := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Mode&07000 != 0 {
			t.Fatal("set-id mode in archive")
		}
		contents[strings.TrimPrefix(h.Name, "r1420-server-openbsd-amd64/")], err = io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(contents) != 4 || !bytes.Equal(contents["manifest.json"], r.files[base+"manifest.json"]) || !bytes.Equal(contents["bin/filees-install"], r.files[base+"bin/filees-install"]) {
		t.Fatalf("archive: %v", contents)
	}
	// Duplicate installation targets must not duplicate downloads or tar entries.
	if r.fetched[base+"bin/filees-install"] != 1 {
		t.Fatal("duplicate payload fetch")
	}
	second, err := p.Publish(context.Background())
	if err != nil || second.Changed {
		t.Fatalf("cached: %+v %v", second, err)
	}
	if r.fetched[base+"bin/filees-install"] != 1 {
		t.Fatal("cache missed")
	}
	// Advancing desktop must retain the independently newer server.
	r.release(t, key, "r6", 6, []byte("new MSI"), "")
	if _, err = p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(filepath.Join(p.OutDir, "index.html"))
	if !strings.Contains(string(page), "r6") || !strings.Contains(string(page), "r1420-server") {
		t.Fatal("mixed identities")
	}
	serverRelease(t, r, key, 1)
	if _, err = p.Publish(context.Background()); err == nil {
		t.Fatal("server rollback accepted")
	}
	after, _ := os.ReadFile(filepath.Join(p.OutDir, "index.html"))
	if !bytes.Equal(page, after) {
		t.Fatal("rollback altered publication")
	}
	// Damage to cached bytes is repaired from verified payloads.
	serverRelease(t, r, key, 1420)
	if err = os.WriteFile(filepath.Join(p.OutDir, archiveName), []byte("damaged"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	repaired, _ := os.ReadFile(filepath.Join(p.OutDir, archiveName))
	if !bytes.Equal(data, repaired) {
		t.Fatal("archive not reproducible")
	}
}

func TestServerFailuresKeepPreviousPublication(t *testing.T) {
	for _, failure := range []string{"channel signature", "manifest signature", "payload", "identity", "conflicting hash", "missing payload"} {
		t.Run(failure, func(t *testing.T) {
			key := newSigner(t)
			r := &repo{files: map[string][]byte{}, fetched: map[string]int{}}
			r.release(t, key, "r5", 5, []byte("MSI"), "")
			p := publisher(t, r, key, t.TempDir())
			if err := os.MkdirAll(filepath.Dir(p.OutDir), 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Publish(context.Background()); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(p.OutDir, "index.html"))
			stateBefore, _ := os.ReadFile(p.StatePath)
			base := serverRelease(t, r, key, 1420)
			p.Config.Server = &ServerConfig{Channel: "alpha", Platform: "openbsd-amd64"}
			switch failure {
			case "channel signature":
				r.files["channels/alpha.json.sig"] = []byte("invalid")
			case "manifest signature":
				r.files[base+"manifest.json.sig"] = []byte("invalid")
			case "payload":
				r.files[base+"bin/filees-install"] = []byte("tampered")
			case "missing payload":
				delete(r.files, base+"bin/filees-install")
			default:
				var m manifest.Manifest
				json.Unmarshal(r.files[base+"manifest.json"], &m)
				if failure == "identity" {
					m.Platform = "linux-amd64"
				} else {
					m.Files[1].SHA256 = strings.Repeat("a", 64)
				}
				raw, _ := json.Marshal(m)
				r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = raw, key.sign(raw)
			}
			if _, err := p.Publish(context.Background()); err == nil {
				t.Fatal("invalid server accepted")
			}
			after, _ := os.ReadFile(filepath.Join(p.OutDir, "index.html"))
			stateAfter, _ := os.ReadFile(p.StatePath)
			if !bytes.Equal(before, after) || !bytes.Equal(stateBefore, stateAfter) {
				t.Fatal("failure altered previous publication/state")
			}
		})
	}
}

// The Linux server is a separate track (alpha-linux). Until its first signed
// release the card is absent and nothing else changes; afterwards the page
// offers its archive, which unpacks into a runnable installation kit.
func TestLinuxServerTrackIsOptionalAndIndependent(t *testing.T) {
	key := newSigner(t)
	r := &repo{files: map[string][]byte{}, fetched: map[string]int{}}
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	serverRelease(t, r, key, 1420)
	p := publisher(t, r, key, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(p.OutDir), 0755); err != nil {
		t.Fatal(err)
	}
	p.Config.Server = &ServerConfig{Channel: "alpha", Platform: "openbsd-amd64"}
	p.Config.ServerLinux = &ServerConfig{Channel: "alpha-linux", Platform: "linux-amd64"}
	p.Template = append([]byte(testTemplate), []byte(`<h2>{{SERVER_RELEASE_ID}}</h2><a href="{{SERVER_FILE}}">{{SERVER_SHA256}}</a>
  <!--SERVER_LINUX-->
<div id="tux">{{SERVER_LINUX_RELEASE_ID}} {{SERVER_LINUX_CHANNEL}} {{SERVER_LINUX_SIZE_PL}} {{SERVER_LINUX_SIGNED_DATE}}<a href="{{SERVER_LINUX_FILE}}">{{SERVER_LINUX_SHA256}}</a>{{SERVER_LINUX_WHATS_NEW_PL}}{{SERVER_LINUX_WHATS_NEW_EN}}</div>
<!--/SERVER_LINUX-->
<p id="after"></p>`)...)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatalf("an unpublished Linux track broke the page: %v", err)
	}
	page, _ := os.ReadFile(filepath.Join(p.OutDir, "index.html"))
	if strings.Contains(string(page), "tux") || strings.Contains(string(page), "SERVER_LINUX") || !strings.Contains(string(page), `<p id="after">`) || !strings.Contains(string(page), "r1420-server") {
		t.Fatalf("page without a Linux release: %s", page)
	}

	id, base := "r9-server-linux", "releases/r9-server-linux/linux-amd64/"
	script, binary := []byte("#!/bin/sh\n"), []byte("ELF")
	m := manifest.Manifest{SchemaVersion: 2, ReleaseID: id, Platform: "linux-amd64", Sequence: 9, SecurityEpoch: 1, Files: []manifest.File{
		{Source: "bin/filees-install", Target: "{sbin_dir}/filees-install", Kind: "binary", Owner: "root", Group: "root", Mode: "0555", SHA256: digest(binary)},
		{Source: "install-server.sh", Target: "/usr/local/share/filees/install-kit/install-server.sh", Kind: "file", Owner: "root", Group: "root", Mode: "0555", SHA256: digest(script)},
		{Source: "linux/install-ssh.sh", Target: "/usr/local/share/filees/install-kit/linux/install-ssh.sh", Kind: "file", Owner: "root", Group: "root", Mode: "0555", SHA256: digest(script)},
	}}
	raw, _ := json.Marshal(m)
	ch, _ := json.Marshal(manifest.Channel{SchemaVersion: 1, ReleaseID: id, Manifest: "releases/" + id + "/{platform}/manifest.json", Sequence: 9, SecurityEpoch: 1})
	r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = raw, key.sign(raw)
	r.files["channels/alpha-linux.json"], r.files["channels/alpha-linux.json.sig"] = ch, key.sign(ch)
	r.files[base+"bin/filees-install"], r.files[base+"linux/install-ssh.sh"], r.files[base+"install-server.sh"] = binary, script, script

	result, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Installers) != 3 {
		t.Fatalf("result: %+v", result)
	}
	page, _ = os.ReadFile(filepath.Join(p.OutDir, "index.html"))
	for _, need := range []string{`<div id="tux">r9-server-linux alpha-linux`, "FileES-r9-server-linux-linux-amd64.tar.gz", "FileES-r1420-server-openbsd-amd64.tar.gz"} {
		if !strings.Contains(string(page), need) {
			t.Errorf("page lacks %s: %s", need, page)
		}
	}
	data, err := os.ReadFile(filepath.Join(p.OutDir, "FileES-r9-server-linux-linux-amd64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	modes := map[string]int64{}
	var readme string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(h.Name, "r9-server-linux-linux-amd64/")
		modes[name] = h.Mode
		if name == "README.txt" {
			body, _ := io.ReadAll(tr)
			readme = string(body)
		}
	}
	if modes["linux/install-ssh.sh"] != 0755 || modes["bin/filees-install"] != 0755 || modes["manifest.json"] != 0644 {
		t.Errorf("archive modes: %v", modes)
	}
	if !strings.Contains(readme, "install-server.sh") || !strings.Contains(readme, "without a sandbox") || !strings.Contains(readme, "alpha-linux") {
		t.Errorf("Linux README does not say how to install: %s", readme)
	}
	sums, _ := os.ReadFile(filepath.Join(p.OutDir, "SHA256SUMS"))
	if !strings.Contains(string(sums), "FileES-r9-server-linux-linux-amd64.tar.gz") {
		t.Errorf("SHA256SUMS lacks the Linux archive: %s", sums)
	}
	// The OpenBSD track going back must still be refused, and the Linux
	// track's own state must refuse its own rollback.
	m.Sequence, ch = 8, nil
	raw, _ = json.Marshal(m)
	ch, _ = json.Marshal(manifest.Channel{SchemaVersion: 1, ReleaseID: id, Manifest: "releases/" + id + "/{platform}/manifest.json", Sequence: 8, SecurityEpoch: 1})
	r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = raw, key.sign(raw)
	r.files["channels/alpha-linux.json"], r.files["channels/alpha-linux.json.sig"] = ch, key.sign(ch)
	if _, err := p.Publish(context.Background()); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("Linux server rollback accepted: %v", err)
	}
}
