package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/releasepublish"
	installmanifest "filees/internal/serverinstall/manifest"
)

// The signed server release carries every manual page, so an upgrade
// installs the pages that describe the binaries it installs.
func TestSignedServerReleaseCarriesEveryManPage(t *testing.T) {
	spec, err := releasepublish.LoadSpec("server/openbsd-binary-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	managed := map[string]releasepublish.FileSpec{}
	for _, file := range spec.Files {
		if strings.HasPrefix(file.Source, "share/man/") {
			managed[file.Source] = file
		}
	}
	pages := manPages(t)
	for _, page := range pages {
		section := filepath.Base(filepath.Dir(page))
		source := "share/man/" + section + "/" + filepath.Base(page)
		file, ok := managed[source]
		if !ok {
			t.Errorf("%s is not in the signed server release", source)
			continue
		}
		delete(managed, source)
		// Absolute: an installer that predates a new target variable would
		// leave it unexpanded. root:wheel 0444 is what install-server.sh
		// leaves behind, so --adopt accepts a fresh installation.
		if file.Target != "/usr/local/man/"+section+"/"+filepath.Base(page) || file.Kind != "file" || file.Mode != "0444" || file.Owner != "root" || file.Group != "wheel" {
			t.Errorf("%s: %+v", source, file)
		}
	}
	for source := range managed {
		t.Errorf("%s is in the release policy but not in docs/man", source)
	}
	script, err := os.ReadFile("../tools/prepare-server-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `"$release_root/share/man/man$section/"`) {
		t.Error("prepare-server-release.sh does not copy the manual pages into the release")
	}
}

// The real policy turns into a manifest the installer accepts, pages included.
func TestServerPolicyGeneratesAnAcceptableManifest(t *testing.T) {
	spec, err := releasepublish.LoadSpec("server/openbsd-binary-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	payload := t.TempDir()
	for _, file := range spec.Files {
		path := filepath.Join(payload, filepath.FromSlash(file.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.Source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec.ReleaseID, spec.Sequence, spec.SecurityEpoch = "r9999-server", 9999, 1
	raw, err := releasepublish.Generate(payload, spec)
	if err != nil {
		t.Fatal(err)
	}
	m, err := installmanifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range m.Files {
		if file.Target == "/usr/local/man/man8/filees-install.8" {
			found = true
		}
	}
	if !found {
		t.Fatal("filees-install(8) is not in the generated manifest")
	}
}
