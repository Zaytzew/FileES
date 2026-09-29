package clientupdate

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/releaseenvelope"
	"filees/pkg/localrepo"
)

func setTestRelease(t *testing.T, installer *DirectoryInstaller, helper bool) *releaseenvelope.Resolved {
	t.Helper()
	contents := completeBundle("new")
	if helper {
		contents["bin/filees-cfapi.exe"] = "new helper"
	}
	var entries []tarEntry
	for name, data := range contents {
		entries = append(entries, tarEntry{name: name, typeflag: tar.TypeReg, mode: 0o755, data: data})
	}
	bundle := makeBundle(t, entries...)
	installer.Stager = BundleStager{Root: t.TempDir(), Fetcher: artifactFetcher{
		"releases/r1/desktop/linux-amd64/client.tar.gz": bundle,
	}}
	return stagedRelease(bundle)
}

func installedHelperFixture(t *testing.T) DirectoryInstaller {
	t.Helper()
	installer := newInstaller(t.TempDir(), "")
	contents := completeBundle("old")
	contents["bin/filees-cfapi.exe"] = "old helper"
	if err := installer.applyStaged(writeBundle(t, contents)); err != nil {
		t.Fatal(err)
	}
	return installer
}

func assertOldInstallation(t *testing.T, installer DirectoryInstaller) {
	t.Helper()
	for name, want := range map[string]string{"filees.exe": "daemon old", "filees-cfapi.exe": "old helper"} {
		got, err := os.ReadFile(filepath.Join(installer.Paths.InstallDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s changed on refusal: %q, %v", name, got, err)
		}
	}
}

func beginTestPoint(t *testing.T, store *localrepo.Store) localrepo.Record {
	t.Helper()
	record, err := store.BeginAnchorAttach("server", "repo", filepath.Join(t.TempDir(), "point"), false)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRetirementPlanAndApply(t *testing.T) {
	installer := installedHelperFixture(t)
	resolved := setTestRelease(t, &installer, false)
	changes, _, err := installer.Plan(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		if change.Path == filepath.Join(installer.Paths.InstallDir, "filees-cfapi.exe") && change.Action == "remove" {
			found = true
		}
	}
	if !found {
		t.Fatal("plan omitted helper removal")
	}
	assertOldInstallation(t, installer)
	if err := installer.Apply(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(installer.Paths.InstallDir, "filees-cfapi.exe")); !os.IsNotExist(err) {
		t.Fatalf("helper still discoverable: %v", err)
	}
	store := installer.Anchors.(*localrepo.Store)
	if _, err := store.BeginAnchorAttach("server", "new", filepath.Join(t.TempDir(), "point"), false); err == nil {
		t.Fatal("new point accepted after helper retirement")
	}
}

func TestRetirementRefusesPointCreatedAfterPlan(t *testing.T) {
	installer := installedHelperFixture(t)
	resolved := setTestRelease(t, &installer, false)
	if _, _, err := installer.Plan(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	store := installer.Anchors.(*localrepo.Store)
	record := beginTestPoint(t, store)
	if err := installer.Apply(context.Background(), resolved); err == nil {
		t.Fatal("apply trusted the obsolete plan")
	}
	assertOldInstallation(t, installer)
	if _, _, err := installer.Plan(context.Background(), resolved); err == nil {
		t.Fatal("plan accepted a pending point")
	}
	if _, err := store.ApproveAttach(record.OperationID, record.ServerID, record.RepoID, "svn+ssh://_filees-client@example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAttached(record.OperationID, record.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(context.Background(), resolved); err == nil {
		t.Fatal("apply accepted an attached point")
	}
	assertOldInstallation(t, installer)
	if _, err := store.BeginDetach(record.ServerID, record.RepoID, false); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(context.Background(), resolved); err == nil {
		t.Fatal("apply accepted an unfinished detach")
	}
	if _, err := store.CompleteDetach(record.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(context.Background(), resolved); err != nil {
		t.Fatalf("completed detach should permit retirement: %v", err)
	}
}

func TestHelperUpdateDoesNotRetireExistingPoints(t *testing.T) {
	installer := installedHelperFixture(t)
	beginTestPoint(t, installer.Anchors.(*localrepo.Store))
	resolved := setTestRelease(t, &installer, true)
	if _, _, err := installer.Plan(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(installer.Paths.InstallDir, "filees-cfapi.exe"))
	if err != nil || string(got) != "new helper" {
		t.Fatalf("helper = %q, %v", got, err)
	}
}

func TestRetirementFailsClosedWithoutLifecycle(t *testing.T) {
	installer := installedHelperFixture(t)
	installer.Anchors = nil
	resolved := setTestRelease(t, &installer, false)
	if _, _, err := installer.Plan(context.Background(), resolved); err == nil {
		t.Fatal("plan without lifecycle accepted")
	}
	if err := installer.Apply(context.Background(), resolved); err == nil {
		t.Fatal("apply without lifecycle accepted")
	}
	assertOldInstallation(t, installer)
}

func TestMissingInstalledHelperDoesNotBypassPointGuard(t *testing.T) {
	installer := newInstaller(t.TempDir(), "")
	beginTestPoint(t, installer.Anchors.(*localrepo.Store))
	resolved := setTestRelease(t, &installer, false)
	if err := installer.Apply(context.Background(), resolved); err == nil {
		t.Fatal("missing helper was mistaken for absence of points")
	}
	if _, err := os.Stat(filepath.Join(installer.Paths.InstallDir, "filees.exe")); !os.IsNotExist(err) {
		t.Fatal("daemon installed despite an existing point")
	}
}

func TestRetirementDoesNotRemoveDirectories(t *testing.T) {
	installer := newInstaller(t.TempDir(), "")
	target := filepath.Join(installer.Paths.InstallDir, "filees-cfapi.exe")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installer.applyStaged(writeBundle(t, completeBundle("new"))); err == nil {
		t.Fatal("directory mistaken for an optional executable")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("directory changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installer.Paths.InstallDir, "filees.exe")); !os.IsNotExist(err) {
		t.Fatal("daemon installed despite invalid retirement target")
	}
}
