package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cliTemplate = `<!--CLI-->
<div>{{CLI_VERSION}} {{CLI_RELEASE_ID}} {{CLI_SIGNED_DATE}}
<a href="{{CLI_LINUX_AMD64_FILE}}">{{CLI_LINUX_AMD64_SHA256}} {{CLI_LINUX_AMD64_SIZE_PL}} {{CLI_LINUX_AMD64_SIZE_EN}}</a>
<a href="{{CLI_OPENBSD_AMD64_FILE}}">{{CLI_OPENBSD_AMD64_SHA256}} {{CLI_OPENBSD_AMD64_SIZE_PL}} {{CLI_OPENBSD_AMD64_SIZE_EN}}</a>
{{CLI_WHATS_NEW_PL}}{{CLI_WHATS_NEW_EN}}</div>
<!--/CLI-->
`

// cliRelease signs one standalone-client release for both platforms and
// points channels/cli.json at it.
func cliRelease(t *testing.T, r *repo, key signer, id string, sequence uint64, archives map[string][]byte) {
	t.Helper()
	var components []map[string]any
	for _, platform := range []string{"linux-amd64", "openbsd-amd64"} {
		base := "releases/" + id + "/cli/" + platform + "/"
		name := "filees-client-" + platform + ".tar.gz"
		data := archives[platform]
		manifest, _ := json.Marshal(map[string]any{
			"schema_version": 2, "release_id": id, "sequence": sequence, "security_epoch": 1, "key_id": "release-test",
			"component": "cli", "platform": platform, "version": "0.1.0",
			"artifacts": []map[string]any{{"source": name, "sha256": digest(data), "size": len(data), "kind": "bundle"}},
		})
		r.files[base+"manifest.json"], r.files[base+"manifest.json.sig"] = manifest, key.sign(manifest)
		r.files[base+name] = data
		components = append(components, map[string]any{"name": "cli", "platform": platform, "manifest": base + "manifest.json"})
	}
	channel, _ := json.Marshal(map[string]any{
		"schema_version": 2, "release_id": id, "sequence": sequence, "security_epoch": 1, "key_id": "release-test",
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), "components": components,
	})
	r.files["channels/cli.json"], r.files["channels/cli.json.sig"] = channel, key.sign(channel)
}

func cliPublisher(t *testing.T, r *repo, key signer) Publisher {
	t.Helper()
	p := publisher(t, r, key, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(p.OutDir), 0755); err != nil {
		t.Fatal(err)
	}
	p.Config.CLI = &CLIConfig{Channel: "cli", Platforms: []string{"openbsd-amd64", "linux-amd64"}}
	p.Template = append([]byte(testTemplate), []byte(cliTemplate)...)
	return p
}

func TestStandaloneClientIsOfferedByteForByteFromItsOwnChannel(t *testing.T) {
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	archives := map[string][]byte{"linux-amd64": []byte("linux archive"), "openbsd-amd64": []byte("openbsd archive")}
	cliRelease(t, r, key, "r7-cli", 7, archives)
	p := cliPublisher(t, r, key)
	first, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || len(first.Installers) != 3 {
		t.Fatalf("result: %+v", first)
	}
	page := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	sums := mustRead(t, filepath.Join(p.OutDir, "SHA256SUMS"))
	for platform, data := range archives {
		name := "FileES-r7-cli-" + platform + ".tar.gz"
		if got := mustRead(t, filepath.Join(p.OutDir, name)); got != string(data) {
			t.Fatalf("%s is not the signed archive: %q", name, got)
		}
		if !strings.Contains(page, `href="`+name+`"`) || !strings.Contains(page, digest(data)) {
			t.Fatalf("page does not offer %s with its digest:\n%s", name, page)
		}
		if !strings.Contains(sums, digest(data)+"  "+name+"\n") {
			t.Fatalf("SHA256SUMS lacks %s:\n%s", name, sums)
		}
	}
	if !strings.Contains(page, "0.1.0 r7-cli 2026-09-16") || strings.Contains(page, "{{") {
		t.Fatalf("card not filled:\n%s", page)
	}
	var state State
	if err := json.Unmarshal([]byte(mustRead(t, p.StatePath)), &state); err != nil {
		t.Fatal(err)
	}
	if state.ReleaseID != "r5" || state.CLI == nil || state.CLI.ReleaseID != "r7-cli" || state.CLI.Sequence != 7 {
		t.Fatalf("state: %+v cli %+v", state, state.CLI)
	}

	// A second run finds the offered bytes still matching and fetches nothing.
	before := r.fetched["releases/r7-cli/cli/linux-amd64/filees-client-linux-amd64.tar.gz"]
	second, err := p.Publish(context.Background())
	if err != nil || second.Changed {
		t.Fatalf("second run: %+v %v", second, err)
	}
	if after := r.fetched["releases/r7-cli/cli/linux-amd64/filees-client-linux-amd64.tar.gz"]; after != before {
		t.Fatalf("archive fetched again: %d -> %d", before, after)
	}

	// The track never goes back, whatever the desktop channel does.
	cliRelease(t, r, key, "r6-cli", 6, archives)
	if _, err := p.Publish(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing cli rollback") {
		t.Fatalf("rollback: %v", err)
	}
}

func TestStandaloneClientCardLeavesThePageWithoutASignedChannel(t *testing.T) {
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	p := cliPublisher(t, r, key)
	result, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	page := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	if len(result.Installers) != 1 || strings.Contains(page, "CLI") {
		t.Fatalf("unpublished track left a trace: %+v\n%s", result, page)
	}
	// Not configured at all: the same template still renders whole.
	p.Config.CLI = nil
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneClientArchiveMustMatchItsSignedManifest(t *testing.T) {
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r5", 5, []byte("MSI"), "")
	cliRelease(t, r, key, "r7-cli", 7, map[string][]byte{"linux-amd64": []byte("linux archive"), "openbsd-amd64": []byte("openbsd archive")})
	r.files["releases/r7-cli/cli/openbsd-amd64/filees-client-openbsd-amd64.tar.gz"] = []byte("openbsd archivX")
	p := cliPublisher(t, r, key)
	if _, err := p.Publish(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match the signed manifest") {
		t.Fatalf("tampered archive: %v", err)
	}
	if _, err := os.Stat(p.OutDir); !os.IsNotExist(err) {
		t.Fatalf("a page was published: %v", err)
	}
}
